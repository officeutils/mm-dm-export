package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

func isExportChannelCommand(args *model.CommandArgs) bool {
	if args == nil {
		return false
	}
	fields := strings.Fields(args.Command)
	return len(fields) > 0 && fields[0] == "/"+channelCommandTrigger
}

func (p *Plugin) executeExportChannelCommand(args *model.CommandArgs) *model.CommandResponse {
	// Channel export is opt-in. Authorization for every supported channel type
	// requires both current membership and the permission to read that channel.
	// Check the opt-in before performing any lookup so a disabled installation
	// cannot disclose or read channel data through this command.
	if !p.channelExportEnabled() {
		return commandError("Channel export is disabled.")
	}
	fields := strings.Fields(args.Command)
	// Dispatch already established that the first field is this command's
	// trigger. Only additional fields are caller-supplied arguments.
	if len(fields) > 1 {
		return commandError("Usage: /export-channel")
	}
	if args.UserId == "" || args.ChannelId == "" {
		return commandError("Unable to export the current channel.")
	}

	channels := p.currentChannelGetter
	if channels == nil {
		channels = p.API
	}
	channel, appErr := channels.GetChannel(args.ChannelId)
	if appErr != nil || channel == nil || channel.Id != args.ChannelId || channel.DeleteAt != 0 ||
		!isSupportedExportChannelType(channel.Type) {
		return commandError("Unable to export the current channel.")
	}

	members := p.memberGetter
	if members == nil {
		members = p.API
	}
	member, appErr := members.GetChannelMember(channel.Id, args.UserId)
	if appErr != nil || !isExpectedMember(member, channel.Id, args.UserId) {
		return commandError("Unable to export the current channel.")
	}
	if channel.Type == model.ChannelTypeDirect {
		participants, appErr := members.GetChannelMembers(channel.Id, 0, 3)
		if appErr != nil || !isTwoParticipantDirectChannel(participants, channel.Id, args.UserId) {
			return commandError("Unable to export the current channel.")
		}
	}

	permissions := p.permissionChecker
	if permissions == nil {
		permissions = p.API
	}
	if !permissions.HasPermissionToChannel(args.UserId, channel.Id, model.PermissionReadChannel) {
		return commandError("Unable to export the current channel.")
	}
	if p.channelExportAccess() == channelExportAdminsOnly {
		users := p.userGetter
		if users == nil {
			users = p.API
		}
		requester, appErr := users.GetUser(args.UserId)
		if appErr != nil || requester == nil || requester.Id != args.UserId || !requester.IsSystemAdmin() {
			return commandError("Unable to export the current channel.")
		}
	}

	postsAPI := p.postGetter
	if postsAPI == nil {
		postsAPI = p.API
	}
	maxPosts := p.maxExportPosts()
	posts, appErr := getSortedChannelPosts(postsAPI, channel.Id, maxPosts)
	if appErr != nil {
		return commandError("Unable to read the current channel.")
	}

	files := p.fileGetter
	if files == nil {
		files = p.API
	}
	attachments, appErr := collectAttachmentMetadata(files, posts)
	if appErr != nil {
		return commandError("Unable to read attachment metadata for the current channel.")
	}

	users := p.userGetter
	if users == nil {
		users = p.API
	}
	authors := resolvePostAuthors(users, posts)
	contents, err := renderChannelHTMLExport(channel, authors, maxPosts, posts, attachments)
	if err != nil {
		return commandError("Unable to render the channel export.")
	}
	if p.exportStore == nil {
		return commandError("Export delivery is temporarily unavailable.")
	}
	exportedAt := time.Now()
	if p.now != nil {
		exportedAt = p.now()
	}
	token, err := p.exportStore.Put(args.UserId, channelExportFilename(channel.Name, exportedAt), contents)
	if err != nil {
		return commandError("Unable to store the channel export. Please download any existing export or try again later.")
	}

	return &model.CommandResponse{ResponseType: "ephemeral", Text: fmt.Sprintf("[Download your channel export](/plugins/%s/download?token=%s). This one-time link expires in 10 minutes.", pluginID, token)}
}

// isSupportedExportChannelType keeps the two export scopes explicit: direct
// messages remain supported, while public and private channels are reachable
// only through the administrator-enabled current-channel command above.
func isSupportedExportChannelType(channelType model.ChannelType) bool {
	return channelType == model.ChannelTypeOpen || channelType == model.ChannelTypePrivate || channelType == model.ChannelTypeDirect
}

func isTwoParticipantDirectChannel(members model.ChannelMembers, channelID, requesterID string) bool {
	if len(members) != 2 {
		return false
	}
	seenRequester := false
	seenUsers := make(map[string]struct{}, 2)
	for _, member := range members {
		if member.ChannelId != channelID || member.UserId == "" {
			return false
		}
		if _, exists := seenUsers[member.UserId]; exists {
			return false
		}
		seenUsers[member.UserId] = struct{}{}
		seenRequester = seenRequester || member.UserId == requesterID
	}
	return seenRequester
}

func resolvePostAuthors(users userGetter, posts []*model.Post) map[string]string {
	authors := make(map[string]string)
	for _, post := range posts {
		if post == nil || post.UserId == "" {
			continue
		}
		if _, seen := authors[post.UserId]; seen {
			continue
		}
		authors[post.UserId] = "Unknown user"
		user, appErr := users.GetUser(post.UserId)
		if appErr == nil && user != nil && user.Id == post.UserId {
			authors[post.UserId] = exportUserName(user)
		}
	}
	return authors
}
