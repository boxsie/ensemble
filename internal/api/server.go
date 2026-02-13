package api

import (
	"context"

	apipb "github.com/boxsie/ensemble/api/pb"
	"github.com/boxsie/ensemble/internal/contacts"
	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/node"
	"github.com/boxsie/ensemble/internal/signaling"
	"github.com/boxsie/ensemble/internal/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NodeProvider is the minimal interface the gRPC server needs from the node.
// Methods are added as subsystems are wired in.
type NodeProvider interface {
	Identity() *identity.Keypair
	Address() identity.Address
	EventBus() *node.EventBus
	TorState() string
	OnionAddr() string
	Connect(ctx context.Context, peerAddr string) (*signaling.HandshakeResult, error)
	AcceptConnection(addr string) error
	RejectConnection(addr string, reason string) error
	PeerCount() int32
	Contacts() *contacts.Store
	ConnectionInfo(addr string) *transport.PeerConnection
	ActiveConnections() []*transport.PeerConnection
	SendMessage(ctx context.Context, addr, text string) (string, error)
	SendFile(ctx context.Context, peerAddr, filePath string) (string, error)
	AcceptFile(transferID, savePath string) error
	RejectFile(transferID, reason string) error
	AddNode(ctx context.Context, onionAddr string) error
}

// Server implements the EnsembleService gRPC interface.
type Server struct {
	apipb.UnimplementedEnsembleServiceServer
	node NodeProvider
}

// NewServer creates a gRPC server backed by the given node.
func NewServer(n NodeProvider) *Server {
	return &Server{node: n}
}

// Register registers this server on a gRPC server instance.
func (s *Server) Register(gs *grpc.Server) {
	apipb.RegisterEnsembleServiceServer(gs, s)
}

// GetIdentity returns the node's address and public key.
func (s *Server) GetIdentity(_ context.Context, _ *apipb.GetIdentityRequest) (*apipb.GetIdentityResponse, error) {
	kp := s.node.Identity()
	return &apipb.GetIdentityResponse{
		Address:   s.node.Address().String(),
		PublicKey: kp.PublicKey(),
	}, nil
}

// GetStatus returns the node's current status.
func (s *Server) GetStatus(_ context.Context, _ *apipb.GetStatusRequest) (*apipb.GetStatusResponse, error) {
	return &apipb.GetStatusResponse{
		TorState:  s.node.TorState(),
		OnionAddr: s.node.OnionAddr(),
		PeerCount: s.node.PeerCount(),
	}, nil
}

// Subscribe streams events from the event bus to the gRPC client.
func (s *Server) Subscribe(_ *apipb.SubscribeRequest, stream grpc.ServerStreamingServer[apipb.DaemonEvent]) error {
	return streamEvents(s.node.EventBus(), stream)
}

// --- Contacts ---

func (s *Server) ListContacts(_ context.Context, _ *apipb.ListContactsRequest) (*apipb.ListContactsResponse, error) {
	cs := s.node.Contacts()
	if cs == nil {
		return &apipb.ListContactsResponse{}, nil
	}
	list := cs.List()
	var out []*apipb.ContactInfo
	for _, c := range list {
		out = append(out, &apipb.ContactInfo{
			Address: c.Address,
			Alias:   c.Alias,
		})
	}
	return &apipb.ListContactsResponse{Contacts: out}, nil
}

func (s *Server) AddContact(_ context.Context, req *apipb.AddContactRequest) (*apipb.AddContactResponse, error) {
	cs := s.node.Contacts()
	if cs == nil {
		return nil, status.Error(codes.FailedPrecondition, "contacts not initialized")
	}
	cs.Add(&contacts.Contact{
		Address: req.Address,
		Alias:   req.Alias,
	})
	if err := cs.Save(); err != nil {
		return nil, status.Errorf(codes.Internal, "saving contacts: %v", err)
	}
	return &apipb.AddContactResponse{}, nil
}

func (s *Server) RemoveContact(_ context.Context, req *apipb.RemoveContactRequest) (*apipb.RemoveContactResponse, error) {
	cs := s.node.Contacts()
	if cs == nil {
		return nil, status.Error(codes.FailedPrecondition, "contacts not initialized")
	}
	cs.Remove(req.Address)
	if err := cs.Save(); err != nil {
		return nil, status.Errorf(codes.Internal, "saving contacts: %v", err)
	}
	return &apipb.RemoveContactResponse{}, nil
}

// --- Connection ---

func (s *Server) Connect(ctx context.Context, req *apipb.ConnectRequest) (*apipb.ConnectResponse, error) {
	result, err := s.node.Connect(ctx, req.Address)
	if err != nil {
		return &apipb.ConnectResponse{
			Accepted: false,
			Message:  err.Error(),
		}, nil
	}
	_ = result
	return &apipb.ConnectResponse{
		Accepted: true,
		Message:  "connected",
	}, nil
}

func (s *Server) AcceptConnection(_ context.Context, req *apipb.AcceptConnectionRequest) (*apipb.AcceptConnectionResponse, error) {
	if err := s.node.AcceptConnection(req.Address); err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return &apipb.AcceptConnectionResponse{}, nil
}

func (s *Server) RejectConnection(_ context.Context, req *apipb.RejectConnectionRequest) (*apipb.RejectConnectionResponse, error) {
	if err := s.node.RejectConnection(req.Address, "rejected by user"); err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return &apipb.RejectConnectionResponse{}, nil
}

func (s *Server) SendMessage(ctx context.Context, req *apipb.SendMessageRequest) (*apipb.SendMessageResponse, error) {
	msgID, err := s.node.SendMessage(ctx, req.Address, req.Text)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "sending message: %v", err)
	}
	return &apipb.SendMessageResponse{MessageId: msgID}, nil
}

func (s *Server) SendFile(req *apipb.SendFileRequest, stream grpc.ServerStreamingServer[apipb.TransferProgress]) error {
	transferID, err := s.node.SendFile(stream.Context(), req.Address, req.FilePath)
	if err != nil {
		return status.Errorf(codes.Internal, "sending file: %v", err)
	}

	// Send a final complete message
	return stream.Send(&apipb.TransferProgress{
		TransferId: transferID,
		Complete:   true,
	})
}

func (s *Server) AcceptFile(_ context.Context, req *apipb.AcceptFileRequest) (*apipb.AcceptFileResponse, error) {
	if err := s.node.AcceptFile(req.TransferId, req.SavePath); err != nil {
		return nil, status.Errorf(codes.Internal, "accepting file: %v", err)
	}
	return &apipb.AcceptFileResponse{}, nil
}

func (s *Server) RejectFile(_ context.Context, req *apipb.RejectFileRequest) (*apipb.RejectFileResponse, error) {
	if err := s.node.RejectFile(req.TransferId, "rejected by user"); err != nil {
		return nil, status.Errorf(codes.Internal, "rejecting file: %v", err)
	}
	return &apipb.RejectFileResponse{}, nil
}

func (s *Server) AddNode(ctx context.Context, req *apipb.AddNodeRequest) (*apipb.AddNodeResponse, error) {
	if err := s.node.AddNode(ctx, req.OnionAddress); err != nil {
		return nil, status.Errorf(codes.Internal, "adding node: %v", err)
	}
	return &apipb.AddNodeResponse{}, nil
}
