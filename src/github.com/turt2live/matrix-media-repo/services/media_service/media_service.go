package media_service

import (
	"context"
	"io"
	"os"
	"strconv"

	"github.com/sirupsen/logrus"
	"github.com/turt2live/matrix-media-repo/config"
	"github.com/turt2live/matrix-media-repo/storage"
	"github.com/turt2live/matrix-media-repo/storage/stores"
	"github.com/turt2live/matrix-media-repo/types"
	"github.com/turt2live/matrix-media-repo/util"
)

type mediaService struct {
	store *stores.MediaStore
	ctx   context.Context
	log   *logrus.Entry
}

func New(ctx context.Context, log *logrus.Entry) (*mediaService) {
	store := storage.GetDatabase().GetMediaStore(ctx, log)
	return &mediaService{store, ctx, log}
}

func (s *mediaService) GetMediaDirect(server string, mediaId string) (*types.Media, error) {
	return s.store.Get(server, mediaId)
}

func (s *mediaService) GetRemoteMediaDirect(server string, mediaId string) (*types.Media, error) {
	return s.downloadRemoteMedia(server, mediaId)
}

func (s *mediaService) downloadRemoteMedia(server string, mediaId string) (*types.Media, error) {
	s.log.Info("Attempting to download remote media")

	result := <-getResourceHandler().DownloadRemoteMedia(server, mediaId)
	return result.media, result.err
}

func (s *mediaService) IsTooLarge(contentLength int64, contentLengthHeader string) (bool) {
	if config.Get().Uploads.MaxSizeBytes <= 0 {
		return false
	}
	if contentLength >= 0 {
		return contentLength > config.Get().Uploads.MaxSizeBytes
	}
	if contentLengthHeader != "" {
		parsed, err := strconv.ParseInt(contentLengthHeader, 10, 64)
		if err != nil {
			s.log.Warn("Invalid content length header given; assuming too large. Value received: " + contentLengthHeader)
			return true // Invalid header
		}

		return parsed > config.Get().Uploads.MaxSizeBytes
	}

	return false // We can only assume
}

func (s *mediaService) UploadMedia(contents io.ReadCloser, contentType string, filename string, userId string, host string) (*types.Media, error) {
	defer contents.Close()
	var data io.Reader
	if config.Get().Uploads.MaxSizeBytes > 0 {
		data = io.LimitReader(contents, config.Get().Uploads.MaxSizeBytes)
	} else {
		data = contents
	}

	return s.StoreMedia(data, contentType, filename, userId, host, "")
}

func (s *mediaService) StoreMedia(contents io.Reader, contentType string, filename string, userId string, host string, mediaId string) (*types.Media, error) {
	isGeneratedId := false
	if mediaId == "" {
		mediaId = generateMediaId()
		isGeneratedId = true
	}
	log := s.log.WithFields(logrus.Fields{
		"mediaService_mediaId":            mediaId,
		"mediaService_host":               host,
		"mediaService_mediaIdIsGenerated": isGeneratedId,
	})

	// Store the file in a temporary location
	fileLocation, err := storage.PersistFile(contents, s.ctx, s.log)
	if err != nil {
		return nil, err
	}

	hash, err := storage.GetFileHash(fileLocation)
	if err != nil {
		defer os.Remove(fileLocation) // attempt cleanup
		return nil, err
	}

	records, err := s.store.GetByHash(hash)
	if err != nil {
		defer os.Remove(fileLocation) // attempt cleanup
		return nil, err
	}

	// If there's at least one record, then we have a duplicate hash - try and process it
	if len(records) > 0 {
		// See if we one of the duplicate records is a match for the host and media ID. We'll otherwise use
		// the last duplicate (should only be 1 anyways) as our starting point for a new record.
		var media *types.Media
		for i := 0; i < len(records); i++ {
			media = records[i]

			if media.Origin == host && (media.MediaId == mediaId || isGeneratedId) {
				if media.ContentType != contentType || media.UserId != userId || media.UploadName != filename {
					// The unique constraint in the database prevents us from storing a duplicate, and we can't generate a new
					// media ID because then we'd be discarding the caller's media ID. In practice, this particular branch would
					// only be executed if a file over federation got changed and we, for some reason, re-downloaded it.
					log.Warn("Match found for media based on host and media ID. Filename, content type, or user ID may not match. Returning unaltered media record")
				} else {
					log.Info("Match found for media based on host and media ID. Returning unaltered media record.")
				}

				overwriteExistingOrDeleteTempFile(fileLocation, media)
				return media, nil
			}

			// The last media object will be used to create a new pointer (normally there should only be one anyways)
		}

		log.Info("Duplicate media hash found, generating a new record using the existing file")

		media.Origin = host
		media.UserId = userId
		media.MediaId = mediaId
		media.UploadName = filename
		media.ContentType = contentType
		media.CreationTs = util.NowMillis()

		err = s.store.Insert(media)
		if err != nil {
			return nil, err
		}

		overwriteExistingOrDeleteTempFile(fileLocation, media)
		return media, nil
	}

	// No duplicate hash, so we have to create a new record

	fileSize, err := util.FileSize(fileLocation)
	if err != nil {
		defer os.Remove(fileLocation) // attempt cleanup
		return nil, err
	}

	log.Info("Persisting unique media record")

	media := &types.Media{
		Origin:      host,
		MediaId:     mediaId,
		UploadName:  filename,
		ContentType: contentType,
		UserId:      userId,
		Sha256Hash:  hash,
		SizeBytes:   fileSize,
		Location:    fileLocation,
		CreationTs:  util.NowMillis(),
	}

	err = s.store.Insert(media)
	if err != nil {
		defer os.Remove(fileLocation) // attempt cleanup
		return nil, err
	}

	return media, nil
}

func generateMediaId() string {
	str, err := util.GenerateRandomString(64)
	if err != nil {
		panic(err)
	}

	return str
}

func overwriteExistingOrDeleteTempFile(tempFileLocation string, media *types.Media) {
	// If the media's file exists, we'll delete the temp file
	// If the media's file doesn't exist, we'll move the temp file to where the media expects it to be
	exists, err := util.FileExists(media.Location)
	if err != nil || !exists {
		// We'll assume an error means it doesn't exist
		os.Rename(tempFileLocation, media.Location)
	} else {
		os.Remove(tempFileLocation)
	}
}