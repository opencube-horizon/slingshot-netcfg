package discover

import (
	"context"
	"errors"
	"time"

	"github.com/vishvananda/netlink"
)

const receiveBuffer = 1 << 20

// Watch subscribes to netlink link and address events and signals trigger
// (non-blocking; trigger should have capacity 1) after every event, after
// every (re)subscription, and after a subscription error, so lost events are
// never assumed harmless (§6.1). Subscription errors are reported to onError
// and the subscription is re-created. Watch returns nil when ctx is done.
func Watch(ctx context.Context, trigger chan<- struct{}, onError func(error)) error {
	for {
		err := watchOnce(ctx, trigger)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // shutting down: errors from the closed subscription are expected.
		}
		onError(err)
		notify(trigger)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}

func watchOnce(ctx context.Context, trigger chan<- struct{}) error {
	done := make(chan struct{})
	links := make(chan netlink.LinkUpdate, 64)
	addrs := make(chan netlink.AddrUpdate, 64)
	errs := make(chan error, 1)
	report := func(err error) {
		select {
		case errs <- err:
		default:
		}
	}
	defer drain(done, links, addrs)

	if err := netlink.LinkSubscribeWithOptions(links, done, netlink.LinkSubscribeOptions{
		ErrorCallback: report, ReceiveBufferSize: receiveBuffer,
	}); err != nil {
		return err
	}
	if err := netlink.AddrSubscribeWithOptions(addrs, done, netlink.AddrSubscribeOptions{
		ErrorCallback: report, ReceiveBufferSize: receiveBuffer,
	}); err != nil {
		return err
	}
	notify(trigger)
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errs:
			return err
		case _, ok := <-links:
			if !ok {
				return errSubscriptionClosed
			}
			notify(trigger)
		case _, ok := <-addrs:
			if !ok {
				return errSubscriptionClosed
			}
			notify(trigger)
		}
	}
}

// drain stops the subscriptions and waits until netlink has closed both
// channels, so no subscription goroutine outlives watchOnce.
func drain(done chan struct{}, links chan netlink.LinkUpdate, addrs chan netlink.AddrUpdate) {
	close(done)
	for links != nil || addrs != nil {
		select {
		case _, ok := <-links:
			if !ok {
				links = nil
			}
		case _, ok := <-addrs:
			if !ok {
				addrs = nil
			}
		}
	}
}

var errSubscriptionClosed = errors.New("netlink subscription closed")

func notify(trigger chan<- struct{}) {
	select {
	case trigger <- struct{}{}:
	default:
	}
}
