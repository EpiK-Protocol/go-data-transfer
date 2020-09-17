package impl

import (
	"bytes"
	"context"
	"fmt"

	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-data-transfer/channels"
	"github.com/filecoin-project/go-data-transfer/encoding"
	"github.com/filecoin-project/go-data-transfer/message"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/libp2p/go-libp2p-core/peer"
	"golang.org/x/xerrors"
)

type ChannelDataTransferType int

const (
	// ManagerPeerCreatePull is the type of a channel wherein the manager peer created a Pull Data Transfer
	ManagerPeerCreatePull ChannelDataTransferType = iota

	// ManagerPeerCreatePush is the type of a channel wherein the manager peer created a Push Data Transfer
	ManagerPeerCreatePush

	// ManagerPeerCreatePush is the type of a channel wherein the manager peer received a Pull Data Transfer Request
	ManagerPeerReceivePull

	// ManagerPeerCreatePush is the type of a channel wherein the manager peer received a Push Data Transfer Request
	ManagerPeerReceivePush
)

func (m *manager) restartManagerPeerReceivePush(ctx context.Context, channel datatransfer.ChannelState) error {
	if err := m.validateRestartVoucher(channel, false); err != nil {
		return xerrors.Errorf("failed to restart channel, validation error: %w", err)
	}

	// send a libp2p message to the other peer asking to send a "restart push request"
	req := message.RestartExistingChannelRequest(channel.ChannelID())

	return m.dataTransferNetwork.SendMessage(ctx, channel.OtherParty(m.peerID), req)
}

func (m *manager) restartManagerPeerReceivePull(ctx context.Context, channel datatransfer.ChannelState) error {
	if err := m.validateRestartVoucher(channel, true); err != nil {
		return xerrors.Errorf("failed to restart channel, validation error: %w", err)
	}

	req := message.RestartExistingChannelRequest(channel.ChannelID())

	// send a libp2p message to the other peer asking to send a "restart pull request"
	return m.dataTransferNetwork.SendMessage(ctx, channel.OtherParty(m.peerID), req)
}

func (m *manager) validateRestartVoucher(channel datatransfer.ChannelState, isPull bool) error {
	// re-validate the original voucher received for safety
	chid := channel.ChannelID()

	// recreate the request that would have led to this pull channel being created for validation
	req, err := message.NewRequest(chid.ID, false, isPull, channel.Voucher().Type(), channel.Voucher(),
		channel.BaseCID(), channel.Selector())
	if err != nil {
		return err
	}

	// revalidate the voucher by reconstructing the request that would have led to the creation of this channel
	if _, _, err := m.validateVoucher(channel.OtherParty(m.peerID), req, isPull, channel.BaseCID(), channel.Selector()); err != nil {
		return err
	}

	return nil
}

func (m *manager) openPushRestartChannel(ctx context.Context, channel datatransfer.ChannelState) error {
	selector := channel.Selector()
	voucher := channel.Voucher()
	baseCid := channel.BaseCID()
	requestTo := channel.OtherParty(m.peerID)
	chid := channel.ChannelID()

	req, err := message.NewRequest(chid.ID, true, false, voucher.Type(), voucher, baseCid, selector)
	if err != nil {
		return err
	}

	processor, has := m.transportConfigurers.Processor(voucher.Type())
	if has {
		transportConfigurer := processor.(datatransfer.TransportConfigurer)
		transportConfigurer(chid, voucher, m.transport)
	}
	m.dataTransferNetwork.Protect(requestTo, chid.String())
	if err := m.dataTransferNetwork.SendMessage(ctx, requestTo, req); err != nil {
		err = fmt.Errorf("Unable to send request: %w", err)
		_ = m.channels.Error(chid, err)
		return err
	}

	return nil
}

func (m *manager) openPullRestartChannel(ctx context.Context, channel datatransfer.ChannelState) error {
	selector := channel.Selector()
	voucher := channel.Voucher()
	baseCid := channel.BaseCID()
	requestTo := channel.OtherParty(m.peerID)
	chid := channel.ChannelID()

	req, err := message.NewRequest(chid.ID, true, true, voucher.Type(), voucher, baseCid, selector)
	if err != nil {
		return err
	}

	processor, has := m.transportConfigurers.Processor(voucher.Type())
	if has {
		transportConfigurer := processor.(datatransfer.TransportConfigurer)
		transportConfigurer(chid, voucher, m.transport)
	}
	m.dataTransferNetwork.Protect(requestTo, chid.String())
	if err := m.transport.OpenChannel(ctx, requestTo, chid, cidlink.Link{Cid: baseCid}, selector, channel.ReceivedCids(), req); err != nil {
		err = fmt.Errorf("Unable to send request: %w", err)
		_ = m.channels.Error(chid, err)
		return err
	}

	return nil
}

func (m *manager) validateRestartRequest(ctx context.Context, otherPeer peer.ID, chid datatransfer.ChannelID, req datatransfer.Request) error {
	// channel should exist
	channel, err := m.channels.GetByID(ctx, chid)
	if err != nil {
		return err
	}

	// channel is not terminated
	if channels.IsChannelTerminated(channel.Status()) {
		return xerrors.New("channel is already terminated")
	}

	// channel initator should be the sender peer
	if channel.ChannelID().Initiator != otherPeer {
		return xerrors.New("other peer is not the initiator of the channel")
	}

	// channel and request baseCid should match
	if req.BaseCid() != channel.BaseCID() {
		return xerrors.New("base cid does not match")
	}

	// vouchers should match
	reqVoucher, err := m.decodeVoucher(req, m.validatedTypes)
	if err != nil {
		return xerrors.Errorf("failed to decode request voucher: %w", err)
	}
	if reqVoucher.Type() != channel.Voucher().Type() {
		return xerrors.New("channel and request voucher types do not match")
	}

	reqBz, err := encoding.Encode(reqVoucher)
	if err != nil {
		return xerrors.New("failed to encode request voucher")
	}
	channelBz, err := encoding.Encode(channel.Voucher())
	if err != nil {
		return xerrors.New("failed to encode channel voucher")
	}

	if !bytes.Equal(reqBz, channelBz) {
		return xerrors.New("channel and request vouchers do not match")
	}

	return nil
}
