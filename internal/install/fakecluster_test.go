package install

import (
	"context"
	"sync/atomic"
	"time"
)

// fakeCluster is a storageCluster with no cluster behind it.
type fakeCluster struct {
	// What storageClasses returns, and how many times it was asked.
	classes    []storageClass
	classesErr error
	classLists int

	// The bundled database's StatefulSet's claim template.
	templateClass, templateSize string
	templateExists              bool

	// claimAt is the claim on the nth read, counting from 1, or nil for none.
	claimAt    func(n int) *claimStatus
	claimReads atomic.Int32

	// The rollout returns rolloutErr after rolloutAfter, or, cancelled first,
	// its context's error.
	rolloutErr       error
	rolloutAfter     time.Duration
	rolloutCancelled atomic.Bool

	flags string
}

func (f *fakeCluster) storageClasses(context.Context) ([]storageClass, error) {
	f.classLists++
	return f.classes, f.classesErr
}

func (f *fakeCluster) databaseClaimTemplate(context.Context, string) (string, string, bool, error) {
	return f.templateClass, f.templateSize, f.templateExists, nil
}

func (f *fakeCluster) claim(context.Context, string, string) (*claimStatus, error) {
	n := int(f.claimReads.Add(1))
	if f.claimAt == nil {
		return nil, nil
	}
	return f.claimAt(n), nil
}

func (f *fakeCluster) waitForRollout(ctx context.Context, _, _, _ string, _ time.Duration) error {
	select {
	case <-ctx.Done():
		f.rolloutCancelled.Store(true)
		return ctx.Err()
	case <-time.After(f.rolloutAfter):
		return f.rolloutErr
	}
}

func (f *fakeCluster) commandFlags() string { return f.flags }

// connect is an interview's connection to its context: this cluster, whatever
// the context.
func (f *fakeCluster) connect(string) storageCluster { return f }
