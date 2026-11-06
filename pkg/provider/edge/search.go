package edge

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// search.* API

const perPage = 100

var errPagination = errors.New("pagination fault")

type searchChannelsResponse struct {
	Pagination pagination `json:"pagination"`
	Items      []Channel  `json:"items"`
}

type searchForm struct {
	Cursor               string            `json:"cursor,omitempty"`
	Module               string            `json:"module"`
	Query                string            `json:"query"`
	Page                 int               `json:"page,omitempty"`
	ClientReqID          string            `json:"client_req_id"`
	BrowseID             string            `json:"browse_session_id"`
	Extracts             int               `json:"extracts"`
	Highlight            int               `json:"highlight"`
	ExtraMsg             int               `json:"extra_message_data"`
	NoUserProfile        int               `json:"no_user_profile"`
	Count                int               `json:"count"`
	FileTitleOnly        bool              `json:"file_title_only"`
	QueryRewriteDisabled bool              `json:"query_rewrite_disabled"`
	IncludeFilesShares   int               `json:"include_files_shares"`
	Browse               string            `json:"browse"`
	SearchContext        string            `json:"search_context"`
	MaxFilterSuggestions int               `json:"max_filter_suggestions"`
	Sort                 searchSortType    `json:"sort"`
	SortDir              searchSortDir     `json:"sort_dir"`
	ChannelType          searchChannelType `json:"channel_type"`
	ExcludeMyChannels    int               `json:"exclude_my_channels"`
	SearchOnlyMyChannels bool              `json:"search_only_my_channels"`
	RecommendSource      string            `json:"recommend_source"`
	WebClientFields
}

type searchChannelType string

const scpAll searchChannelType = ""

type searchSortDir string

const ssdAsc searchSortDir = "asc"

type searchSortType string

const sstName searchSortType = "name"

func (cl *Client) searchChannels(ctx context.Context, query string) ([]slack.Channel, error) {
	lg := cl.logger.With(zap.String("in", "SearchChannels"), zap.String("query", query))

	clientReq, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	browseID, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	form := searchForm{
		Module:               "channels",
		Query:                query,
		ClientReqID:          clientReq.String(),
		BrowseID:             browseID.String(),
		Cursor:               "*",
		NoUserProfile:        1,
		Count:                perPage,
		IncludeFilesShares:   1,
		Browse:               "standard",
		SearchContext:        "desktop_channel_browser",
		MaxFilterSuggestions: 10,
		Sort:                 sstName,
		SortDir:              ssdAsc,
		ChannelType:          scpAll,
		RecommendSource:      "channel-browser",
		WebClientFields:      webclientReason("browser-query"),
	}

	const (
		ep                 = "search.modules.channels"
		maxSearchPages     = 100
		maxEmptyQueryPages = 5 // empty query is used for cache warm; avoid full-workspace crawl
	)
	maxPages := maxSearchPages
	if query == "" {
		maxPages = maxEmptyQueryPages
	}
	var cc []slack.Channel
	seenCursor := make(map[string]struct{})
	for page := 0; page < maxPages; page++ {
		var sr searchChannelsResponse
		if err := cl.postForm(ctx, ep, form, &sr); err != nil {
			return nil, err
		}
		cc = append(cc, slackChannels(sr.Items)...)
		if sr.Pagination.NextCursor == "" {
			lg.Debug("no more channels")
			break
		}
		if _, dup := seenCursor[sr.Pagination.NextCursor]; dup {
			return nil, errPagination
		}
		seenCursor[sr.Pagination.NextCursor] = struct{}{}
		lg.Debug("pagination", zap.String("next_cursor", sr.Pagination.NextCursor))
		form.Cursor = sr.Pagination.NextCursor
		if page == maxPages-1 {
			lg.Warn("SearchChannels page cap reached with remaining cursor; results may be incomplete",
				zap.Int("max_pages", maxPages),
				zap.Int("channels", len(cc)),
				zap.String("next_cursor", sr.Pagination.NextCursor),
				zap.Bool("empty_query", query == ""),
			)
			break
		}
	}
	lg.Debug("channels", zap.Int("count", len(cc)))
	return cc, nil
}
