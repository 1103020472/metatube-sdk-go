package route

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/1103020472/metatube-sdk-go/engine"
)

func getDBVersion(app *engine.Engine) gin.HandlerFunc {
	return func(c *gin.Context) {
		version, err := app.DBVersion()
		if err != nil {
			abortWithStatusMessage(c, http.StatusInternalServerError, err)
			return
		}
		c.JSON(http.StatusOK, &responseMessage{
			Data: gin.H{
				"version": version,
			},
		})
	}
}
