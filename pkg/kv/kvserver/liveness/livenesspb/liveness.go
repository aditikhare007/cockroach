// Copyright 2020 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package livenesspb

import (
	"fmt"
	"time"

	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/util/hlc"
	"github.com/cockroachdb/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// IsLive returns whether the node is considered live at the given time.
//
// NOTE: If one is interested whether the Liveness is valid currently, then the
// timestamp passed in should be the known high-water mark of all the clocks of
// the nodes in the cluster. For example, if the liveness expires at ts 100, our
// physical clock is at 90, but we know that another node's clock is at 110,
// then it's preferable (more consistent across nodes) for the liveness to be
// considered expired. For that purpose, it's better to pass in
// clock.Now().GoTime() rather than clock.PhysicalNow() - the former takes into
// consideration clock signals from other nodes, the latter doesn't.
func (l *Liveness) IsLive(now hlc.Timestamp) bool {
	return now.Less(l.Expiration.ToTimestamp())
}

// IsDead returns true if the liveness expired more than threshold ago.
//
// Note that, because of threshold, IsDead() is not the inverse of IsLive().
func (l *Liveness) IsDead(now hlc.Timestamp, threshold time.Duration) bool {
	expiration := l.Expiration.ToTimestamp().AddDuration(threshold)
	return !now.Less(expiration)
}

// Compare returns an integer comparing two pieces of liveness information,
// based on which liveness information is more recent.
func (l *Liveness) Compare(o Liveness) int {
	// Compare Epoch, and if no change there, Expiration.
	if l.Epoch != o.Epoch {
		if l.Epoch < o.Epoch {
			return -1
		}
		return +1
	}
	if !l.Expiration.EqOrdering(o.Expiration) {
		if l.Expiration.Less(o.Expiration) {
			return -1
		}
		return +1
	}
	return 0
}

func (l Liveness) String() string {
	var extra string
	if l.Draining || l.Membership.Decommissioning() || l.Membership.Decommissioned() {
		extra = fmt.Sprintf(" drain:%t membership:%s", l.Draining, l.Membership.String())
	}
	return fmt.Sprintf("liveness(nid:%d epo:%d exp:%s%s)", l.NodeID, l.Epoch, l.Expiration, extra)
}

// Decommissioning is a shorthand to check if the membership status is DECOMMISSIONING.
func (c MembershipStatus) Decommissioning() bool { return c == MembershipStatus_DECOMMISSIONING }

// Decommissioned is a shorthand to check if the membership status is DECOMMISSIONED.
func (c MembershipStatus) Decommissioned() bool { return c == MembershipStatus_DECOMMISSIONED }

// Active is a shorthand to check if the membership status is ACTIVE.
func (c MembershipStatus) Active() bool { return c == MembershipStatus_ACTIVE }

func (c MembershipStatus) String() string {
	// NB: These strings must not be changed, since the CLI matches on them.
	switch c {
	case MembershipStatus_ACTIVE:
		return "active"
	case MembershipStatus_DECOMMISSIONING:
		return "decommissioning"
	case MembershipStatus_DECOMMISSIONED:
		return "decommissioned"
	default:
		err := "unknown membership status, expected one of [active,decommissioning,decommissioned]"
		panic(err)
	}
}

// ValidateTransition validates transitions of the liveness record,
// returning an error if the proposed transition is invalid. Ignoring no-ops
// (which also includes decommissioning a decommissioned node) the valid state
// transitions for Membership are as follows:
//
//	Decommissioning  => Active
//	Active           => Decommissioning
//	Decommissioning  => Decommissioned
//
// This returns an error if the transition is invalid, and false if the
// transition is unnecessary (since it would be a no-op).
func ValidateTransition(old Liveness, newStatus MembershipStatus) (bool, error) {
	if (old == Liveness{}) {
		return false, errors.AssertionFailedf("invalid old liveness record; found to be empty")
	}

	if old.Membership == newStatus {
		// No-op.
		return false, nil
	}

	if old.Membership.Decommissioned() && newStatus.Decommissioning() {
		// No-op as it would just move directly back to decommissioned.
		return false, nil
	}

	if newStatus.Active() && !old.Membership.Decommissioning() {
		err := fmt.Sprintf("can only recommission a decommissioning node; n%d found to be %s",
			old.NodeID, old.Membership.String())
		return false, status.Error(codes.FailedPrecondition, err)
	}

	// We don't assert on the new membership being "decommissioning" as all
	// previous states are valid (again, consider no-ops).

	if newStatus.Decommissioned() && !old.Membership.Decommissioning() {
		err := fmt.Sprintf("can only fully decommission an already decommissioning node; n%d found to be %s",
			old.NodeID, old.Membership.String())
		return false, status.Error(codes.FailedPrecondition, err)
	}

	return true, nil
}

// IsLiveMapEntry encapsulates data about current liveness for a
// node.
type IsLiveMapEntry struct {
	Liveness
	IsLive bool
}

// IsLiveMap is a type alias for a map from NodeID to IsLiveMapEntry.
type IsLiveMap map[roachpb.NodeID]IsLiveMapEntry
