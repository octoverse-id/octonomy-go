package octonomy

import (
	"net/url"
	"strconv"
)

// ListOptions carries the limit/offset paging controls shared by every list
// endpoint. Embed it in a resource's *ListParams. The server defaults to a limit
// of 50 and caps it at 200.
type ListOptions struct {
	Limit  int
	Offset int
}

func (o ListOptions) apply(q url.Values) {
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		q.Set("offset", strconv.Itoa(o.Offset))
	}
}

// Pagination is the metadata block returned alongside every list response.
// Next and Previous are absolute URLs for the adjacent pages, or nil at an edge.
type Pagination struct {
	Limit    int     `json:"limit"`
	Offset   int     `json:"offset"`
	Count    int     `json:"count"`
	Next     *string `json:"next"`
	Previous *string `json:"previous"`
}

// The list envelope every Octonomy endpoint returns -- {"data": [...],
// "pagination": {...}} -- is declared once per resource on this line, as TagList
// (tags.go) and VocabularyList (vocabularies.go), instead of the generic List[T]
// the modern line uses. Type parameters need Go 1.18 and this line targets Go
// 1.13, so a new resource repeats the two-field struct rather than instantiating
// a shared one. Pagination and ListOptions above stay shared and identical
// across both lines.
