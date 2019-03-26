package packager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"

	"github.com/deislabs/duffle/pkg/bundle"
	"github.com/deislabs/duffle/pkg/loader"
	"github.com/deislabs/duffle/pkg/signature"
)

type Exporter struct {
	Source        string
	Destination   string
	Thin          bool
	Client        *client.Client
	Context       context.Context
	Logs          string
	Loader        loader.Loader
	Unsigned      bool
	Signer        string
	SecretKeyRing string
}

// NewExporter returns an *Exporter given information about where a bundle
//  lives, where the compressed bundle should be exported to,
//  and what form a bundle should be exported in (thin or thick/full). It also
//  sets up a docker client to work with images.
func NewExporter(source, dest, logsDir string, l loader.Loader, thin, unsigned bool, signer, secretKeyRing string) (*Exporter, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	cli.NegotiateAPIVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot negotiate Docker client version: %v", err)
	}

	logs := filepath.Join(logsDir, "export-"+time.Now().Format("20060102150405"))

	return &Exporter{
		Source:        source,
		Destination:   dest,
		Thin:          thin,
		Client:        cli,
		Context:       ctx,
		Logs:          logs,
		Loader:        l,
		Unsigned:      unsigned,
		Signer:        signer,
		SecretKeyRing: secretKeyRing,
	}, nil
}

// Export prepares an artifacts directory containing all of the necessary
//  images, packages the bundle along with the artifacts in a gzipped tar
//  file, and saves that file to the file path specified as destination.
//  If the any part of the destination path doesn't, it will be created.
//  exist
func (ex *Exporter) Export() error {

	//prepare log file for this export
	logsf, err := os.Create(ex.Logs)
	if err != nil {
		return err
	}
	defer logsf.Close()

	fi, err := os.Stat(ex.Source)
	if os.IsNotExist(err) {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("Bundle manifest %s is a directory, should be a file", ex.Source)
	}

	bun, err := ex.Loader.Load(ex.Source)
	if err != nil {
		return fmt.Errorf("Error loading bundle: %s", err)
	}
	name := bun.Name + "-" + bun.Version
	archiveDir, err := filepath.Abs(name + "-export")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		return err
	}
	defer os.RemoveAll(archiveDir)

	from, err := os.Open(ex.Source)
	if err != nil {
		return err
	}
	defer from.Close()

	bundlefile := "bundle.cnab"
	if ex.Unsigned {
		bundlefile = "bundle.json"
	}
	to, err := os.OpenFile(filepath.Join(archiveDir, bundlefile), os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return err
	}
	defer to.Close()

	_, err = io.Copy(to, from)
	if err != nil {
		return err
	}
	to.Close()

	if !ex.Thin {
		if err := ex.prepareArtifacts(bun, archiveDir, logsf); err != nil {
			return fmt.Errorf("Error preparing artifacts: %s", err)
		}
		fmt.Printf("\n updated bundle:\n%#v\n", bun) //TODO remove

		//update bundle with shasums
		if err := writeBundle(bun, ex.Signer, ex.SecretKeyRing, filepath.Join(archiveDir, bundlefile)); err != nil {
			return fmt.Errorf("Error writing updated bundle file: %s", err)
		}
	}

	dest := name + ".tgz"
	if ex.Destination != "" {
		dest = ex.Destination
	}

	writer, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("Error creating archive file: %s", err)
	}

	defer writer.Close()

	tarOptions := &archive.TarOptions{
		Compression:      archive.Gzip,
		IncludeFiles:     []string{"."},
		IncludeSourceDir: true,
	}
	rc, err := archive.TarWithOptions(archiveDir, tarOptions)
	if err != nil {
		return err
	}
	defer rc.Close()

	_, err = io.Copy(writer, rc)
	return err
}

// prepareArtifacts pulls all images, verifies their digests (TODO: verify digest) and
//  saves them to a directory called artifacts/ in the bundle directory
func (ex *Exporter) prepareArtifacts(bun *bundle.Bundle, archiveDir string, logs io.Writer) error {
	artifactsDir := filepath.Join(archiveDir, "artifacts")
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return err
	}

	imagesWithShasums := map[string]bundle.Image{}
	for key, image := range bun.Images {
		_, checksum, err := ex.archiveImage(image.Image, artifactsDir, logs)
		if err != nil {
			return err
		}

		image.Digest = checksum
		imagesWithShasums[key] = image
	}
	bun.Images = imagesWithShasums

	invocationImagesWithShasums := []bundle.InvocationImage{}
	for _, in := range bun.InvocationImages {
		_, checksum, err := ex.archiveImage(in.Image, artifactsDir, logs)
		if err != nil {
			return err
		}
		in.Digest = checksum
		invocationImagesWithShasums = append(invocationImagesWithShasums, in)
	}

	bun.InvocationImages = invocationImagesWithShasums

	return nil
}

func (ex *Exporter) archiveImage(image, artifactsDir string, logs io.Writer) (string, string, error) {
	ctx := ex.Context

	imagePullOptions := types.ImagePullOptions{} //TODO: add platform info
	pullLogs, err := ex.Client.ImagePull(ctx, image, imagePullOptions)
	if err != nil {
		return "", "", fmt.Errorf("Error pulling image %s: %s", image, err)
	}
	defer pullLogs.Close()
	io.Copy(logs, pullLogs)

	reader, err := ex.Client.ImageSave(ctx, []string{image})
	if err != nil {
		return "", "", err
	}
	defer reader.Close()
	//TODO: get checksum

	name := buildFileName(image) + ".tar"
	out, err := os.Create(filepath.Join(artifactsDir, name))
	if err != nil {
		return name, "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, reader); err != nil {
		return name, "", err
	}

	checksum := "tempchecksum"
	return name, checksum, nil
}

func buildFileName(uri string) string {
	filename := strings.Replace(uri, "/", "-", -1)
	return strings.Replace(filename, ":", "-", -1)

}

func writeBundle(bf *bundle.Bundle, signer, secretKeyRing, outputFile string) error {
	kr, err := signature.LoadKeyRing(secretKeyRing)
	if err != nil {
		return fmt.Errorf("cannot load keyring: %s", err)
	}

	if kr.Len() == 0 {
		return errors.New("no signing keys are present in the keyring")
	}

	// Default to the first key in the ring unless the user specifies otherwise.
	key := kr.Keys()[0]
	if signer != "" {
		key, err = kr.Key(signer)
		if err != nil {
			return err
		}
	}

	sign := signature.NewSigner(key)
	data, err := sign.Clearsign(bf)
	data = append(data, '\n')
	if err != nil {
		return fmt.Errorf("cannot sign bundle: %s", err)
	}

	if err := ioutil.WriteFile(outputFile, data, 0644); err != nil {
		return fmt.Errorf("cannot write bundle to %s: %v", outputFile, err)
	}

	return nil
}
