// Package config holds config.json (hub name, listen address).
package config

import (
	"net"
	"strconv"

	"github.com/azamrizki/nextboard/internal/store"
)

const (
	File        = "config.json"
	DefaultPort = 12312
	// DefaultListen is loopback: the hub speaks plain HTTP and HTTPS ends at
	// a reverse proxy on the same host (deploy/nginx.conf.example).
	DefaultListen = "127.0.0.1:12312"
)

// Config is persisted in <data>/config.json.
type Config struct {
	HubName string `json:"hubName"`
	Listen  string `json:"listen"`
	// ExtraURLs are advertised to paired apps first (the public HTTPS URL).
	ExtraURLs []string `json:"extraUrls,omitempty"`
}

// Load reads config.json, filling defaults.
func Load(st *store.Store) (*Config, error) {
	c := &Config{}
	if _, err := st.Load(File, c); err != nil {
		return nil, err
	}
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.ExtraURLs == nil {
		c.ExtraURLs = []string{}
	}
	return c, nil
}

// Save writes config.json.
func (c *Config) Save(st *store.Store) error { return st.Save(File, c) }

// Port returns the numeric port of Listen.
func (c *Config) Port() int {
	_, p, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return DefaultPort
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return DefaultPort
	}
	return n
}
