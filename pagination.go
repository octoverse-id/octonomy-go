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

// List is the generic envelope every Octonomy list endpoint returns:
// {"data": [...], "pagination": {...}}.
type List[T any] struct {
	Data       []T        `json:"data"`
	Pagination Pagination `json:"pagination"`
}
