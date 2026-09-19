package main

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
)

const (
	commandTrigger          = "export-dm"
	channelCommandTrigger   = "export-channel"
	pluginID                = "com.officeutils.mm-conversation-export"
	defaultMaxExportPosts   = 1000
	maxExportPostsSafety    = 10000
	postPageSize            = 200
	channelExportAllMembers = "all_members"
	channelExportAdminsOnly = "admins_only"
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type commandRegistrar interface {
	RegisterCommand(command *model.Command) error
}

type userGetter interface {
	GetUser(userID string) (*model.User, *model.AppError)
	GetUserByUsername(username string) (*model.User, *model.AppError)
}

type channelGetter interface {
	GetChannelsForTeamForUser(teamID, userID string, includeDeleted bool) ([]*model.Channel, *model.AppError)
}

type currentChannelGetter interface {
	GetChannel(channelID string) (*model.Channel, *model.AppError)
}

type channelPermissionChecker interface {
	HasPermissionToChannel(userID, channelID string, permission *model.Permission) bool
}

type channelMemberGetter interface {
	GetChannelMember(channelID, userID string) (*model.ChannelMember, *model.AppError)
	GetChannelMembers(channelID string, page, perPage int) (model.ChannelMembers, *model.AppError)
}

type channelPostGetter interface {
	GetPostsForChannel(channelID string, page, perPage int) (*model.PostList, *model.AppError)
}

type fileInfoGetter interface {
	GetFileInfo(fileID string) (*model.FileInfo, *model.AppError)
}

type configurationLoader interface {
	LoadPluginConfiguration(dest any) error
}

// Plugin is the server-side conversation export plugin.
type Plugin struct {
	plugin.MattermostPlugin

	commandRegistrar     commandRegistrar
	userGetter           userGetter
	channelGetter        channelGetter
	currentChannelGetter currentChannelGetter
	memberGetter         channelMemberGetter
	permissionChecker    channelPermissionChecker
	postGetter           channelPostGetter
	fileGetter           fileInfoGetter
	configurationLoader  configurationLoader
	exportStore          temporaryExportStore
	now                  func() time.Time
	configurationMu      sync.RWMutex
	configuration        configuration
}

type configuration struct {
	MaxExportPosts      string
	EnableChannelExport bool
	ChannelExportAccess string
}

func (p *Plugin) channelExportAccess() string {
	p.configurationMu.RLock()
	defer p.configurationMu.RUnlock()
	if p.configuration.ChannelExportAccess == channelExportAllMembers {
		return channelExportAllMembers
	}
	// Empty and unknown values fail closed to the default, restrictive mode.
	return channelExportAdminsOnly
}

func (p *Plugin) maxExportPosts() int {
	p.configurationMu.RLock()
	value := p.configuration.MaxExportPosts
	p.configurationMu.RUnlock()
	if value == "" {
		return defaultMaxExportPosts
	}
	limit, err := parseMaxExportPosts(value)
	if err != nil {
		return defaultMaxExportPosts
	}
	return limit
}

func (p *Plugin) channelExportEnabled() bool {
	p.configurationMu.RLock()
	defer p.configurationMu.RUnlock()
	return p.configuration.EnableChannelExport
}

func parseMaxExportPosts(value string) (int, error) {
	limit, err := strconv.Atoi(value)
	if err != nil || limit <= 0 || limit > maxExportPostsSafety {
		return 0, fmt.Errorf("MaxExportPosts must be a positive integer no greater than %d", maxExportPostsSafety)
	}
	return limit, nil
}

// OnConfigurationChange validates and applies System Console changes without a rebuild.
func (p *Plugin) OnConfigurationChange() error {
	var next configuration
	loader := p.configurationLoader
	if loader == nil {
		loader = p.API
	}
	if err := loader.LoadPluginConfiguration(&next); err != nil {
		return err
	}
	if next.MaxExportPosts == "" {
		next.MaxExportPosts = strconv.Itoa(defaultMaxExportPosts)
	}
	if _, err := parseMaxExportPosts(next.MaxExportPosts); err != nil {
		return err
	}
	if next.ChannelExportAccess != channelExportAllMembers && next.ChannelExportAccess != channelExportAdminsOnly {
		next.ChannelExportAccess = channelExportAdminsOnly
	}
	p.configurationMu.Lock()
	p.configuration = next
	p.configurationMu.Unlock()
	return nil
}

// OnActivate registers the slash command exposed by the plugin.
func (p *Plugin) OnActivate() error {
	if p.exportStore == nil {
		p.exportStore = newDefaultMemoryExportStore()
	}

	registrar := p.commandRegistrar
	if registrar == nil {
		registrar = p.API
	}

	if err := registrar.RegisterCommand(&model.Command{
		Trigger:          commandTrigger,
		AutoComplete:     true,
		AutoCompleteDesc: "Export a direct-message conversation",
		AutoCompleteHint: "@username",
	}); err != nil {
		return err
	}

	return registrar.RegisterCommand(&model.Command{
		Trigger:          channelCommandTrigger,
		AutoComplete:     true,
		AutoCompleteDesc: "Export the current public or private channel",
	})
}

// ServeHTTP delivers requester-bound exports through Mattermost's authenticated
// plugin route. Mattermost removes any client-provided Mattermost-User-Id header
// and supplies it from the authenticated session before invoking this hook.
func (p *Plugin) ServeHTTP(_ *plugin.Context, w http.ResponseWriter, r *http.Request) {
	setDownloadResponseHeaders(w.Header())

	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/download" {
		http.NotFound(w, r)
		return
	}

	requesterID := r.Header.Get("Mattermost-User-Id")
	if requesterID == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	tokens, present := r.URL.Query()["token"]
	if !present || len(tokens) != 1 || tokens[0] == "" {
		http.NotFound(w, r)
		return
	}
	if p.exportStore == nil {
		http.Error(w, "export delivery unavailable", http.StatusServiceUnavailable)
		return
	}

	token := tokens[0]
	export, err := p.exportStore.Claim(requesterID, token)
	if err != nil {
		// Ownership failures, expired tokens, invalid tokens, and replays are
		// intentionally indistinguishable to callers.
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, export.filename))
	w.WriteHeader(http.StatusOK)
	written, writeErr := w.Write(export.contents)
	p.exportStore.Finish(requesterID, token, writeErr == nil && written == len(export.contents))
}

func setDownloadResponseHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store, no-cache, must-revalidate")
	header.Set("Pragma", "no-cache")
	header.Set("Expires", "0")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "sandbox; default-src 'none'")
}

// ExecuteCommand validates an export request and locates its existing direct
// channel. Mattermost supplies UserId from the authenticated command request.
func (p *Plugin) ExecuteCommand(_ *plugin.Context, args *model.CommandArgs) (*model.CommandResponse, *model.AppError) {
	if isExportChannelCommand(args) {
		return p.executeExportChannelCommand(args), nil
	}

	if args == nil || args.UserId == "" {
		return commandError("Unable to export direct messages without an authenticated requester."), nil
	}

	username, err := parseCommandUsername(args.Command)
	if err != nil {
		return commandError("Usage: /export-dm @username"), nil
	}

	users := p.userGetter
	if users == nil {
		users = p.API
	}

	requester, appErr := users.GetUser(args.UserId)
	if appErr != nil || requester == nil {
		return commandError("Unable to resolve the authenticated requester."), nil
	}

	target, appErr := users.GetUserByUsername(username)
	if appErr != nil || target == nil {
		return commandError(fmt.Sprintf("Unable to find user @%s.", username)), nil
	}

	if requester.Id == target.Id {
		return commandError("You cannot export a direct-message conversation with yourself."), nil
	}

	channels := p.channelGetter
	if channels == nil {
		channels = p.API
	}

	requesterChannels, appErr := channels.GetChannelsForTeamForUser("", requester.Id, false)
	if appErr != nil {
		return commandError("Unable to inspect your direct-message conversations."), nil
	}

	directChannel := findDirectChannel(requesterChannels, requester.Id, target.Id)
	if directChannel == nil {
		return commandError(fmt.Sprintf("No direct-message conversation with @%s exists.", username)), nil
	}

	members := p.memberGetter
	if members == nil {
		members = p.API
	}

	if !authorizeDirectChannel(members, directChannel.Id, requester.Id, target.Id) {
		return commandError("Unable to authorize that direct-message conversation."), nil
	}

	posts := p.postGetter
	if posts == nil {
		posts = p.API
	}

	maxPosts := p.maxExportPosts()
	sortedPosts, appErr := getSortedChannelPosts(posts, directChannel.Id, maxPosts)
	if appErr != nil {
		return commandError("Unable to read that direct-message conversation."), nil
	}

	files := p.fileGetter
	if files == nil {
		files = p.API
	}

	attachments, appErr := collectAttachmentMetadata(files, sortedPosts)
	if appErr != nil {
		return commandError("Unable to read attachment metadata for that direct-message conversation."), nil
	}

	exportedAt := time.Now()
	if p.now != nil {
		exportedAt = p.now()
	}
	contents, err := renderHTMLExport(requester, target, exportedAt, maxPosts, sortedPosts, attachments)
	if err != nil {
		return commandError("Unable to render that direct-message export."), nil
	}
	if p.exportStore == nil {
		return commandError("Export delivery is temporarily unavailable."), nil
	}
	filename := exportFilename(requester.Username, target.Username, exportedAt)
	token, err := p.exportStore.Put(requester.Id, filename, contents)
	if err != nil {
		return commandError("Unable to store that direct-message export. Please download any existing export or try again later."), nil
	}

	downloadURL := fmt.Sprintf("/plugins/%s/download?token=%s", pluginID, token)
	return &model.CommandResponse{
		ResponseType: "ephemeral",
		Text:         fmt.Sprintf("[Download your direct-message export with @%s](%s). This one-time link expires in 10 minutes.", username, downloadURL),
	}, nil
}

func getSortedChannelPosts(posts channelPostGetter, channelID string, limit int) ([]*model.Post, *model.AppError) {
	unique := make(map[string]*model.Post, limit)
	perPage := min(postPageSize, limit)
	for page := 0; len(unique) < limit; page++ {
		postList, appErr := posts.GetPostsForChannel(channelID, page, perPage)
		if appErr != nil {
			return nil, appErr
		}
		if postList == nil {
			return nil, model.NewAppError("getSortedChannelPosts", "received an empty post list", nil, "", 500)
		}
		for _, postID := range postList.Order {
			if post := postList.Posts[postID]; post != nil {
				unique[post.Id] = post
				if len(unique) == limit {
					break
				}
			}
		}
		if len(postList.Order) < perPage {
			break
		}
	}
	sortedPosts := make([]*model.Post, 0, len(unique))
	for _, post := range unique {
		sortedPosts = append(sortedPosts, post)
	}

	sort.Slice(sortedPosts, func(i, j int) bool {
		if sortedPosts[i].CreateAt != sortedPosts[j].CreateAt {
			return sortedPosts[i].CreateAt < sortedPosts[j].CreateAt
		}
		return sortedPosts[i].Id < sortedPosts[j].Id
	})

	return sortedPosts, nil
}

func authorizeDirectChannel(members channelMemberGetter, channelID, requesterID, targetID string) bool {
	requesterMember, appErr := members.GetChannelMember(channelID, requesterID)
	if appErr != nil || !isExpectedMember(requesterMember, channelID, requesterID) {
		return false
	}

	targetMember, appErr := members.GetChannelMember(channelID, targetID)
	if appErr != nil || !isExpectedMember(targetMember, channelID, targetID) {
		return false
	}

	// Fetch at most three members: a third result is enough to reject a channel
	// that is not the expected two-person conversation.
	channelMembers, appErr := members.GetChannelMembers(channelID, 0, 3)
	if appErr != nil || len(channelMembers) != 2 {
		return false
	}

	seen := map[string]bool{}
	for _, member := range channelMembers {
		if member.ChannelId != channelID ||
			(member.UserId != requesterID && member.UserId != targetID) || seen[member.UserId] {
			return false
		}
		seen[member.UserId] = true
	}

	return seen[requesterID] && seen[targetID]
}

func isExpectedMember(member *model.ChannelMember, channelID, userID string) bool {
	return member != nil && member.ChannelId == channelID && member.UserId == userID
}

func findDirectChannel(channels []*model.Channel, requesterID, targetID string) *model.Channel {
	directChannelName := model.GetDMNameFromIds(requesterID, targetID)
	for _, channel := range channels {
		if channel != nil && channel.Type == model.ChannelTypeDirect && channel.Name == directChannelName {
			return channel
		}
	}

	return nil
}

func parseCommandUsername(command string) (string, error) {
	fields := strings.Fields(command)
	if len(fields) != 2 || fields[0] != "/"+commandTrigger {
		return "", fmt.Errorf("expected /%s followed by one username", commandTrigger)
	}

	username := strings.TrimPrefix(fields[1], "@")
	if !usernamePattern.MatchString(username) {
		return "", fmt.Errorf("invalid username")
	}

	return username, nil
}

func commandError(message string) *model.CommandResponse {
	return &model.CommandResponse{
		ResponseType: "ephemeral",
		Text:         message,
	}
}

func main() {
	plugin.ClientMain(&Plugin{})
}
