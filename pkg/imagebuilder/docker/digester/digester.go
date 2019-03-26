package digester

import (
	"golang.org/x/net/context"

	"github.com/docker/docker/client"
	"github.com/opencontainers/go-digest"
)

type Digester struct {
	Client  client.ImageAPIClient
	Image   string
	Context context.Context
}

func NewDigester(client client.ImageAPIClient, image string, ctx context.Context) *Digester {
	return &Digester{
		Client:  client,
		Image:   image,
		Context: ctx,
	}
}

// Digest returns the digest of the image tar
func (d *Digester) Digest() (string, error) {
	reader, err := d.Client.ImageSave(d.Context, []string{d.Image})
	computedDigest, err := digest.Canonical.FromReader(reader)
	if err != nil {
		return "", err
	}
	defer reader.Close()

	return computedDigest.String(), nil
}
