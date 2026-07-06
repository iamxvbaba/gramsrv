package rpc

import (
	"net/url"

	"telesrv/internal/links"
)

func (r *Router) publicLink(path string) string {
	return publicLinkWithBaseURL(r.cfg.PublicBaseURL, path)
}

func (r *Router) publicLinkQuery(path string, query url.Values) string {
	return publicLinkQueryWithBaseURL(r.cfg.PublicBaseURL, path, query)
}

func (r *Router) publicLinkParam(path, key, value string) string {
	return r.publicLinkQuery(path, url.Values{key: []string{value}})
}

func (r *Router) publicLinkHost() string {
	return links.Host(r.cfg.PublicBaseURL)
}

func publicLinkWithBaseURL(baseURL, path string) string {
	return links.Build(baseURL, path, nil)
}

func publicLinkQueryWithBaseURL(baseURL, path string, query url.Values) string {
	return links.Build(baseURL, path, query)
}

func publicLinkParamWithBaseURL(baseURL, path, key, value string) string {
	return publicLinkQueryWithBaseURL(baseURL, path, url.Values{key: []string{value}})
}
