package controllers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
)

// idParam reads the path parameter named name from c, decodes it as a
// public token of type t, and returns the underlying internal integer id.
// On a missing/malformed/mistyped token it writes a 400 JSON error response
// and returns (0, false); callers must check the second return value and
// return immediately when it is false, e.g.:
//
//	id, ok := idParam(c, "id", publicid.TypeExam)
//	if !ok {
//	    return
//	}
func idParam(c *gin.Context, name string, t publicid.Type) (int, bool) {
	raw := c.Param(name)

	id, err := publicid.Decode(t, raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + name + " id"})
		return 0, false
	}

	return id, true
}
