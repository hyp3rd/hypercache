package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// startTopologyRefresh launches the background loop that periodically
// pulls /cluster/members and replaces the in-memory endpoint view
// with the alive-or-suspect members' API addresses. Called once
// from New when WithTopologyRefresh was set with a positive
// interval.
func (c *Client) startTopologyRefresh() {
	c.refreshStopCh = make(chan struct{})
	c.refreshDoneCh = make(chan struct{})

	go c.topologyRefreshLoop()

	c.logger.Info(
		"client topology refresh started",
		slog.Duration("interval", c.refreshInterval),
		slog.Int("seeds", len(c.seeds)),
	)
}

// topologyRefreshLoop drives the refresh ticker. Exits when
// refreshStopCh is closed. Refresh failures are logged at Warn but
// don't tear down the loop — a transient outage shouldn't cost the
// client its periodic recovery cadence.
func (c *Client) topologyRefreshLoop() {
	defer close(c.refreshDoneCh)

	ticker := time.NewTicker(c.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.refreshStopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), c.refreshInterval)

			err := c.RefreshTopology(ctx)

			cancel()

			if err != nil {
				c.logger.Warn(
					"client topology refresh failed",
					slog.Any("err", err),
				)
			}
		}
	}
}

// RefreshTopology synchronously pulls /cluster/members from one of
// the currently-known endpoints (random pick, fail over to seeds
// if the working view is empty) and updates the client's endpoint
// view in place. Returns the error from the underlying call when
// every attempt failed; the in-memory view is left unchanged on
// failure so the next call can fall back to the same endpoints.
//
// Exposed for tests and operator-driven refreshes (e.g. after a
// known node join). The background loop calls this on its own
// tick; manual callers usually don't need to.
func (c *Client) RefreshTopology(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/cluster/members", nil, map[string]string{
		"Accept": contentTypeJSON,
	})
	if err != nil {
		return fmt.Errorf("fetch /cluster/members: %w", err)
	}
	defer closeBody(resp)

	var members clusterMembersResponse

	decodeErr := json.NewDecoder(resp.Body).Decode(&members)
	if decodeErr != nil {
		return fmt.Errorf("decode /cluster/members: %w", decodeErr)
	}

	// Project the live membership into endpoint URLs. We need a
	// scheme to prepend to the host:port the membership snapshot
	// reports — borrow it from the first seed (operators typically
	// use a homogeneous scheme across the cluster).
	scheme := schemeFromSeeds(c.seeds)
	updated := make([]string, 0, len(members.Members))

	for _, m := range members.Members {
		if m.Address == "" {
			continue
		}

		// Dead members get pruned by the server's heartbeat
		// before they show up here, but be defensive — never
		// dispatch to a known-dead endpoint.
		if m.State == "dead" {
			continue
		}

		endpoint := buildEndpoint(scheme, m.Address)
		if endpoint != "" {
			updated = append(updated, endpoint)
		}
	}

	if len(updated) == 0 {
		// Membership returned nothing usable. Don't wipe the
		// in-memory view — leaving the existing endpoints lets
		// the next request still reach the cluster while the
		// next refresh tick tries again.
		return nil
	}

	c.endpoints.Store(&updated)
	c.logger.Debug(
		"client topology refreshed",
		slog.Int("members", len(updated)),
	)

	return nil
}

// schemeFromSeeds extracts the URL scheme from the first valid
// seed. Falls back to "https" when no seed parses — production
// deployments should always be TLS, and a seed without a scheme
// is most likely an operator config error we don't want to silently
// downgrade to plaintext.
func schemeFromSeeds(seeds []string) string {
	for _, s := range seeds {
		u, err := url.Parse(s)
		if err != nil {
			continue
		}

		if u.Scheme != "" {
			return u.Scheme
		}
	}

	return "https"
}

// buildEndpoint composes a base URL from a scheme and a host:port.
// The membership snapshot reports addresses as host:port (matching
// the server's HYPERCACHE_API_ADDR shape); we prepend the scheme
// to produce a URL the rest of the client can use directly.
func buildEndpoint(scheme, address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}

	if strings.Contains(address, "://") {
		// Already a full URL — operator pre-baked the scheme into
		// the membership entry. Trust it.
		return strings.TrimRight(address, "/")
	}

	return scheme + "://" + address
}
