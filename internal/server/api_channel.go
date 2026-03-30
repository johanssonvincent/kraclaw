package server

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/johanssonvincent/kraclaw/internal/channel"
	"github.com/johanssonvincent/kraclaw/internal/channel/tui"
	"github.com/johanssonvincent/kraclaw/internal/store"
	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

type channelService struct {
	kraclawv1.UnimplementedChannelServiceServer

	tui      *tui.TUI
	channels []channel.Channel
	store    store.Store
	log      *slog.Logger
}

func (s *channelService) SendMessage(_ context.Context, req *kraclawv1.SendMessageRequest) (*kraclawv1.SendMessageResponse, error) {
	if s.tui == nil {
		return nil, status.Error(codes.Unavailable, "TUI channel not configured")
	}
	if req.ChatJid == "" {
		return nil, status.Error(codes.InvalidArgument, "chat_jid is required")
	}
	if req.Text == "" {
		return nil, status.Error(codes.InvalidArgument, "text is required")
	}

	if err := s.tui.HandleInbound(req.ChatJid, req.Sender, req.SenderName, req.Text); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "channel not ready: %v", err)
	}
	s.log.Info("inbound TUI message", "jid", req.ChatJid, "sender", req.Sender)
	return &kraclawv1.SendMessageResponse{}, nil
}

func (s *channelService) StreamInbound(req *kraclawv1.StreamInboundRequest, stream grpc.ServerStreamingServer[kraclawv1.InboundMessage]) error {
	if s.tui == nil {
		return status.Error(codes.Unavailable, "TUI channel not configured")
	}
	if req.ChatJid == "" {
		return status.Error(codes.InvalidArgument, "chat_jid is required")
	}

	if modelName := s.lookupModel(stream.Context(), req.ChatJid); modelName != "" {
		if err := stream.Send(&kraclawv1.InboundMessage{
			ChatJid: req.ChatJid,
			Channel: "tui",
			Model:   modelName,
		}); err != nil {
			return err
		}
	}

	ch, unsubscribe := s.tui.Subscribe(req.ChatJid)
	defer unsubscribe()

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case text, ok := <-ch:
			if !ok {
				s.log.Info("inbound stream closed", "chat_jid", req.ChatJid)
				return status.Error(codes.Aborted, "channel closed by server")
			}
			msg := &kraclawv1.InboundMessage{
				Content: text,
				ChatJid: req.ChatJid,
				Channel: "tui",
				Model:   s.lookupModel(stream.Context(), req.ChatJid),
			}
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

func (s *channelService) lookupModel(ctx context.Context, chatJID string) string {
	if s.store == nil {
		return ""
	}
	group, err := s.store.GetGroup(ctx, chatJID)
	if err != nil || group == nil || group.ContainerConfig == nil {
		return ""
	}
	return group.ContainerConfig.Model
}

func (s *channelService) ListChannels(_ context.Context, _ *kraclawv1.ListChannelsRequest) (*kraclawv1.ListChannelsResponse, error) {
	resp := &kraclawv1.ListChannelsResponse{}
	for _, ch := range s.channels {
		resp.Channels = append(resp.Channels, &kraclawv1.ChannelStatus{
			Name:      ch.Name(),
			Connected: ch.IsConnected(),
		})
	}
	return resp, nil
}
