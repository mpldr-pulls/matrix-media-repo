package url_previewing

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/ryanuber/go-glob"
	"github.com/turt2live/matrix-media-repo/common"
	"github.com/turt2live/matrix-media-repo/common/rcontext"
	"github.com/turt2live/matrix-media-repo/util"
	"github.com/turt2live/matrix-media-repo/util/readers"
)

func doHttpGet(urlPayload *UrlPayload, languageHeader string, ctx rcontext.RequestContext) (*http.Response, error) {
	var client *http.Client

	dialer := &net.Dialer{
		Timeout:   time.Duration(ctx.Config.TimeoutSeconds.UrlPreviews) * time.Second,
		KeepAlive: time.Duration(ctx.Config.TimeoutSeconds.UrlPreviews) * time.Second,
		DualStack: true,
	}

	dialContext := func(ctx2 context.Context, network, addr string) (conn net.Conn, e error) {
		if network != "tcp" {
			return nil, errors.New("invalid network: expected tcp")
		}

		safeIp, safePort, err := getSafeAddress(addr, ctx)
		if err != nil {
			return nil, err
		}

		// Try and determine which port we're expecting a request to come in on. Because the
		// http library follows redirects, we should also keep track of the alternate port
		// so that redirects don't fail previews. We only support the alternate port if the
		// default port for the scheme is used, however.

		altPort := ""
		if safePort == "" {
			if urlPayload.ParsedUrl.Scheme == "http" {
				safePort = "80"
				altPort = "443"
			} else if urlPayload.ParsedUrl.Scheme == "https" {
				safePort = "443"
				altPort = "80"
			} else {
				return nil, errors.New("unexpected scheme: cannot determine port")
			}
		}

		safeIpStr := safeIp.String()

		expectedAddr := net.JoinHostPort(urlPayload.ParsedUrl.Host, safePort)
		altAddr := net.JoinHostPort(urlPayload.ParsedUrl.Host, altPort)

		returnAddr := ""
		if addr == expectedAddr {
			returnAddr = net.JoinHostPort(safeIpStr, safePort)
		} else if addr == altAddr && altPort != "" {
			returnAddr = net.JoinHostPort(safeIpStr, altPort)
		}

		if returnAddr != "" {
			return dialer.DialContext(ctx, network, returnAddr)
		}

		return nil, errors.New("unexpected host: not safe to complete request")
	}

	if ctx.Config.UrlPreviews.UnsafeCertificates {
		ctx.Log.Warn("Ignoring any certificate errors while making request")
		tr := &http.Transport{
			DisableKeepAlives: true,
			DialContext:       dialContext,
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
			// Based on https://github.com/matrix-org/gomatrixserverlib/blob/51152a681e69a832efcd934b60080b92bc98b286/client.go#L74-L90
			DialTLS: func(network, addr string) (net.Conn, error) {
				rawconn, err := net.Dial(network, addr)
				if err != nil {
					return nil, err
				}
				// Wrap a raw connection ourselves since tls.Dial defaults the SNI
				conn := tls.Client(rawconn, &tls.Config{
					ServerName:         "",
					InsecureSkipVerify: true,
				})
				if err := conn.Handshake(); err != nil {
					return nil, err
				}
				return conn, nil
			},
		}
		client = &http.Client{
			Transport: tr,
			Timeout:   time.Duration(ctx.Config.TimeoutSeconds.UrlPreviews) * time.Second,
		}
	} else {
		client = &http.Client{
			Timeout: time.Duration(ctx.Config.TimeoutSeconds.UrlPreviews) * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives: true,
				DialContext:       dialContext,
			},
		}
	}

	req, err := http.NewRequest("GET", urlPayload.ParsedUrl.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ctx.Config.UrlPreviews.UserAgent)
	req.Header.Set("Accept-Language", languageHeader)
	return client.Do(req)
}

func downloadRawContent(urlPayload *UrlPayload, supportedTypes []string, languageHeader string, ctx rcontext.RequestContext) (io.ReadCloser, string, string, error) {
	ctx.Log.Info("Fetching remote content...")
	resp, err := doHttpGet(urlPayload, languageHeader, ctx)
	if err != nil {
		return nil, "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		ctx.Log.Warn("Received status code " + strconv.Itoa(resp.StatusCode))
		return nil, "", "", errors.New("error during transfer")
	}

	if ctx.Config.UrlPreviews.MaxPageSizeBytes > 0 && resp.ContentLength >= 0 && resp.ContentLength > ctx.Config.UrlPreviews.MaxPageSizeBytes {
		return nil, "", "", common.ErrMediaTooLarge
	}

	var reader io.ReadCloser
	if ctx.Config.UrlPreviews.MaxPageSizeBytes > 0 {
		lr := io.LimitReader(resp.Body, ctx.Config.UrlPreviews.MaxPageSizeBytes)
		reader = readers.NewCancelCloser(io.NopCloser(lr), func() {
			resp.Body.Close()
		})
	}

	contentType := resp.Header.Get("Content-Type")
	for _, supportedType := range supportedTypes {
		if !glob.Glob(supportedType, contentType) {
			return nil, "", "", ErrPreviewUnsupported
		}
	}

	disposition := resp.Header.Get("Content-Disposition")
	_, params, _ := mime.ParseMediaType(disposition)
	filename := ""
	if params != nil {
		filename = params["filename"]
	}

	return reader, filename, contentType, nil
}

func downloadHtmlContent(urlPayload *UrlPayload, supportedTypes []string, languageHeader string, ctx rcontext.RequestContext) (string, error) {
	r, _, contentType, err := downloadRawContent(urlPayload, supportedTypes, languageHeader, ctx)
	if err != nil {
		return "", err
	}
	html := ""
	defer r.Close()
	raw, _ := io.ReadAll(r)
	if raw != nil {
		html = util.ToUtf8(string(raw), contentType)
	}
	return html, nil
}

func downloadImage(urlPayload *UrlPayload, languageHeader string, ctx rcontext.RequestContext) (*Image, error) {
	ctx.Log.Info("Getting image from " + urlPayload.ParsedUrl.String())
	resp, err := doHttpGet(urlPayload, languageHeader, ctx)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		ctx.Log.Warn("Received status code " + strconv.Itoa(resp.StatusCode))
		return nil, errors.New("error during transfer")
	}

	image := &Image{
		ContentType: resp.Header.Get("Content-Type"),
		Data:        resp.Body,
	}

	_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	if err == nil && params["filename"] != "" {
		image.Filename = params["filename"]
	}

	return image, nil
}
