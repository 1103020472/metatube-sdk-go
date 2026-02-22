package duga

import (
	"testing"

	"github.com/1103020472/metatube-sdk-go/provider/internal/testkit"
)

func TestDUGA_GetMovieInfoByID(t *testing.T) {
	testkit.Test(t, New, []string{
		"glory-4262",
		"waap-1294",
	})
}

func TestDUGA_SearchMovie(t *testing.T) {
	testkit.Test(t, New, []string{
		"DINM",
	})
}
