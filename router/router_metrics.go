package router

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/nycto-sh/wings/config"
	"github.com/nycto-sh/wings/internal/metrics"
	"github.com/nycto-sh/wings/router/middleware"
	"github.com/nycto-sh/wings/server"
)

const (
	defaultMetricsPoints = 300
	maxMetricsPoints     = 1000
)

// getServerMetrics returns the resource usage history of a server, summarized
// into points for a graph.
//
// @see docs/metrics.md and protocol/WINGS_API.md §10.3.
func getServerMetrics(c *gin.Context) {
	cfg := config.Get().System.Metrics
	if !cfg.Enabled {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "The metrics history is turned off on this node."})
		return
	}
	interval, samples := cfg.MetricsSettings()

	to := time.Now().Unix()
	if v := c.Query("to"); v != "" {
		t, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "The to parameter must be a Unix timestamp."})
			return
		}
		to = t
	}
	from := to - 24*3600
	if v := c.Query("range"); v != "" {
		d, err := parseMetricsRange(v)
		if err != nil || d <= 0 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "The range parameter must be a duration such as 1h, 24h or 7d."})
			return
		}
		from = to - int64(d/time.Second)
	}
	if v := c.Query("from"); v != "" {
		t, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "The from parameter must be a Unix timestamp."})
			return
		}
		from = t
	}
	if from > to {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "The from time must be before the to time."})
		return
	}
	// Nothing older than the history kept exists.
	from = max(from, to-interval*samples)

	points := defaultMetricsPoints
	if v := c.Query("points"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxMetricsPoints {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "The points parameter must be a number from 1 to 1000."})
			return
		}
		points = n
	}

	s := middleware.ExtractServer(c)
	history, err := s.MetricsHistory().Read(from, to)
	if err != nil {
		middleware.CaptureAndAbort(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"interval": interval,
		"from":     from,
		"to":       to,
		"points":   metrics.Summarize(history, from, to, interval, points),
	})
}

// parseMetricsRange parses a duration such as "90m", "24h" or "7d".
func parseMetricsRange(v string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		return time.Duration(n) * 24 * time.Hour, err
	}
	return time.ParseDuration(v)
}

// getPrometheusMetrics serves the resource usage of every server on the node in
// the Prometheus text format.
func getPrometheusMetrics(c *gin.Context) {
	if !config.Get().System.Metrics.Prometheus {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Prometheus metrics are turned off on this node."})
		return
	}
	var b strings.Builder
	server.WritePrometheus(&b, middleware.ExtractManager(c).All())
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(b.String()))
}
