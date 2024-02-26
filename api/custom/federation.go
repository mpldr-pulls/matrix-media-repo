package custom

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/getsentry/sentry-go"
	"github.com/t2bot/matrix-media-repo/api/_apimeta"
	"github.com/t2bot/matrix-media-repo/api/_responses"
	"github.com/t2bot/matrix-media-repo/api/_routers"

	"github.com/sirupsen/logrus"
	"github.com/t2bot/matrix-media-repo/common/rcontext"
	"github.com/t2bot/matrix-media-repo/matrix"
)

func GetFederationInfo(r *http.Request, rctx rcontext.RequestContext, user _apimeta.UserInfo) interface{} {
	serverName := _routers.GetParam("serverName", r)

	if !_routers.ServerNameRegex.MatchString(serverName) {
		return _responses.BadRequest(errors.New("invalid server name"))
	}

	rctx = rctx.LogWithFields(logrus.Fields{
		"serverName": serverName,
	})

	url, hostname, err := matrix.GetServerApiUrl(serverName)
	if err != nil {
		rctx.Log.Error(err)
		sentry.CaptureException(err)
		return _responses.InternalServerError(err)
	}

	versionUrl := url + "/_matrix/federation/v1/version"
	versionResponse, err := matrix.FederatedGet(versionUrl, hostname, rctx)
	if err != nil {
		rctx.Log.Error(err)
		sentry.CaptureException(err)
		return _responses.InternalServerError(err)
	}

	decoder := json.NewDecoder(versionResponse.Body)
	out := make(map[string]interface{})
	err = decoder.Decode(&out)
	if err != nil {
		rctx.Log.Error(err)
		sentry.CaptureException(err)
		return _responses.InternalServerError(err)
	}

	resp := make(map[string]interface{})
	resp["base_url"] = url
	resp["hostname"] = hostname
	resp["versions_response"] = out
	return &_responses.DoNotCacheResponse{Payload: resp}
}
