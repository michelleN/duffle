package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/docker/cli/cli/config"
	"github.com/docker/cnab-to-oci/remotes"
	dref "github.com/docker/distribution/reference"
	"github.com/spf13/cobra"

	"github.com/deislabs/duffle/pkg/bundle"
	"github.com/deislabs/duffle/pkg/loader"
	"github.com/deislabs/duffle/pkg/reference"
)

const pullUsage = `Pulls a CNAB bundle into the cache without installing it. `

var ErrNotSigned = errors.New("bundle is not signed")

type pullCmd struct {
	output             string
	targetRef          string
	insecureRegistries []string
}

func newPullCmd(w io.Writer) *cobra.Command {
	pull := &pullCmd{}

	cmd := &cobra.Command{
		Use:   "pull",
		Short: "pull a CNAB bundle from a repository",
		Long:  pullUsage,
		RunE: func(cmd *cobra.Command, args []string) error {
			return pull.run()
		},
	}

	f := cmd.Flags()
	f.StringSliceVar(&pull.insecureRegistries, "insecure-registries", nil, "Use plain HTTP for those registries")
	f.StringVarP(&pull.output, "output", "o", "", "output file")

	return cmd
}

func createResolver(insecureRegistries []string) remotes.ResolverConfig {
	return remotes.NewResolverConfigFromDockerConfigFile(config.LoadDefaultConfigFile(os.Stderr), insecureRegistries...)
}

func (p *pullCmd) run() error {
	ref, err := dref.ParseNormalizedNamed(p.targetRef)
	if err != nil {
		return err
	}
	b, err := remotes.Pull(context.Background(), ref, createResolver(p.insecureRegistries).Resolver)
	if err != nil {
		return err
	}
	//TODO save it to local store after marshalling
	bytes, err := json.MarshalIndent(b, "", "\t")
	if err != nil {
		return err
	}
	if p.output == "" {
		fmt.Fprintln(os.Stdout, string(bytes))
		return nil
	}
	return nil
	//return ioutil.WriteFile(opts.output, bytes, 0644)
}

func getLoader(home string, insecure bool) (loader.Loader, error) {
	var load loader.Loader
	if insecure {
		load = loader.NewDetectingLoader()
	} else {
		kr, err := loadVerifyingKeyRings(home)
		if err != nil {
			return nil, fmt.Errorf("cannot securely load bundle: %s", err)
		}
		load = loader.NewSecureLoader(kr)
	}
	return load, nil
}

func getReference(bundleName string) (reference.NamedTagged, error) {
	var (
		name string
		ref  reference.NamedTagged
	)

	parts := strings.SplitN(bundleName, "://", 2)
	if len(parts) == 2 {
		name = parts[1]
	} else {
		name = parts[0]
	}
	normalizedRef, err := reference.ParseNormalizedNamed(name)
	if err != nil {
		return nil, fmt.Errorf("%q is not a valid bundle name: %v", name, err)
	}
	if reference.IsNameOnly(normalizedRef) {
		ref, err = reference.WithTag(normalizedRef, "latest")
		if err != nil {
			// NOTE(bacongobbler): Using the default tag *must* be valid.
			// To create a NamedTagged type with non-validated
			// input, the WithTag function should be used instead.
			panic(err)
		}
	} else {
		if taggedRef, ok := normalizedRef.(reference.NamedTagged); ok {
			ref = taggedRef
		} else {
			return nil, fmt.Errorf("unsupported image name: %s", normalizedRef.String())
		}
	}

	return ref, nil
}

func loadBundle(bundleFile string, insecure bool) (*bundle.Bundle, error) {
	l, err := getLoader(homePath(), insecure)
	if err != nil {
		return nil, err
	}
	// Issue #439: Errors that come back from the loader can be
	// pretty opaque.
	var bun *bundle.Bundle
	if bun, err = l.Load(bundleFile); err != nil {
		if err.Error() == "no signature block in data" {
			return bun, ErrNotSigned
		}
		// Dear Go, Y U NO TERNARY, kthxbye
		secflag := "secure"
		if insecure {
			secflag = "insecure"
		}
		return bun, fmt.Errorf("cannot load %s bundle: %s", secflag, err)
	}
	return bun, nil
}
