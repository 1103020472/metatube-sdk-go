package muramura

import (
	"regexp"

	"github.com/1103020472/metatube-sdk-go/model"
	"github.com/1103020472/metatube-sdk-go/provider"
	"github.com/1103020472/metatube-sdk-go/provider/1pondo/core"
)

var (
	_ provider.MovieProvider = (*MuraMura)(nil)
	_ provider.MovieReviewer = (*MuraMura)(nil)
)

const (
	Name     = "MURAMURA"
	Priority = 1000 - 1
)

const (
	baseURL        = "https://www.muramura.tv/"
	movieURL       = "https://www.muramura.tv/movies/%s/"
	sampleVideoURL = "https://fms.muramura.tv/sample/%s/mb.m3u8"
)

type MuraMura struct {
	*core.Core
}

func New() *MuraMura {
	return &MuraMura{
		Core: (&core.Core{
			BaseURL:           baseURL,
			MovieURL:          movieURL,
			SampleVideoURL:    sampleVideoURL,
			DefaultName:       Name,
			DefaultPriority:   Priority,
			DefaultMaker:      "ムラムラってくる素人",
			GalleryPath:       "",
			LegacyGalleryPath: "",
		}).Init(),
	}
}

func (ppm *MuraMura) GetMovieReviewsByID(_ string) ([]*model.MovieReviewDetail, error) {
	return nil, nil // no reviews provided.
}

func (ppm *MuraMura) NormalizeMovieID(id string) string {
	if regexp.MustCompile(`^\d{6}_\d{3}$`).MatchString(id) {
		return id
	}
	return ""
}

func init() {
	provider.Register(Name, New)
}
