package imagebuilder

import (
	"context"
	"io"
)

// ImageBuilder contains the information to build an image
type ImageBuilder interface {
	Name() string
	Type() string
	URI() string
	Digest() (string, error)

	PrepareBuild(string, string, string) error
	Build(context.Context, io.WriteCloser) (string, error)
}
