package preview

import (
	"fmt"
	"io"

	"github.com/t2bot/matrix-media-repo/common/rcontext"
	"golang.org/x/image/webp"
)

type webpGenerator struct{}

func (d webpGenerator) supportedContentTypes() []string {
	return []string{"image/webp"}
}

func (d webpGenerator) supportsAnimation() bool {
	return true
}

func (d webpGenerator) matches(img io.Reader, contentType string) bool {
	return contentType == "image/webp"
}

func (d webpGenerator) GetOriginDimensions(b io.Reader, contentType string, ctx rcontext.RequestContext) (bool, int, int, error) {
	i, err := webp.DecodeConfig(b)
	if err != nil {
		return false, 0, 0, err
	}
	return true, i.Width, i.Height, nil
}

func (d webpGenerator) GenerateThumbnail(b io.Reader, contentType string, width int, height int, method string, animated bool, ctx rcontext.RequestContext) (*Thumbnail, error) {
	src, err := webp.Decode(b)
	if err != nil {
		return nil, fmt.Errorf("webp: error decoding thumbnail: %w", err)
	}

	return pngGenerator{}.GenerateThumbnailOf(src, width, height, method, ctx)
}

func init() {
	generators = append(generators, webpGenerator{})
}
