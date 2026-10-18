package route

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/1103020472/metatube-sdk-go/engine"
	mt "github.com/1103020472/metatube-sdk-go/provider"
)

type providerConfigQuery struct {
	Provider string `form:"provider" binding:"required"`
	Key      string `form:"key" binding:"required"`
	Value    string `form:"value"`
}

// setProviderConfig updates a movie provider's config at runtime,
// e.g. POST /v1/providers/config?provider=javdb&key=cookie&value=_jdb_session%3D...
// Value is optional: leaving it empty clears the key from the config.
func setProviderConfig(app *engine.Engine) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := &providerConfigQuery{}
		if err := c.ShouldBindQuery(query); err != nil {
			abortWithStatusMessage(c, http.StatusBadRequest, err)
			return
		}

		// Base the new config on the current one so that unrelated keys
		// (proxy/timeout/priority) survive partial updates.
		config := mt.NewEditableConfig()
		if current, ok := app.GetMovieProviderConfig(query.Provider); ok {
			if editable, ok := current.(mt.EditableConfig); ok {
				for k, v := range editable.Iterator() {
					config.Set(k, v)
				}
			}
		}
		if query.Value == "" {
			config.Delete(query.Key)
		} else {
			config.Set(query.Key, query.Value)
		}

		if err := app.SetMovieProviderConfig(query.Provider, config); err != nil {
			abortWithStatusMessage(c, http.StatusInternalServerError, err)
			return
		}

		c.JSON(http.StatusOK, &responseMessage{Data: gin.H{
			"provider": query.Provider,
			"key":      query.Key,
			"updated":  query.Value != "",
		}})
	}
}
