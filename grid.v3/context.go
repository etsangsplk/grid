package grid

import (
	"context"

	etcdv3 "github.com/coreos/etcd/clientv3"
	"github.com/lytics/grid/grid.v3/discovery"
	"github.com/lytics/grid/grid.v3/message"
)

func ContextActorID(c context.Context) (string, error) {
	v := c.Value(contextKey)
	if v == nil {
		return "", ErrInvalidContext
	}
	cv, ok := v.(*contextVal)
	if !ok {
		return "", ErrInvalidContext
	}
	return cv.actorID, nil
}

func ContextActorName(c context.Context) (string, error) {
	v := c.Value(contextKey)
	if v == nil {
		return "", ErrInvalidContext
	}
	cv, ok := v.(*contextVal)
	if !ok {
		return "", ErrInvalidContext
	}
	return cv.actorName, nil
}

func ContextNamespace(c context.Context) (string, error) {
	v := c.Value(contextKey)
	if v == nil {
		return "", ErrInvalidContext
	}
	cv, ok := v.(*contextVal)
	if !ok {
		return "", ErrInvalidContext
	}
	return cv.r.g.Namespace(), nil
}

func ContextEtcd(c context.Context) (*etcdv3.Client, error) {
	v := c.Value(contextKey)
	if v == nil {
		return nil, ErrInvalidContext
	}
	cv, ok := v.(*contextVal)
	if !ok {
		return nil, ErrInvalidContext
	}
	return cv.r.etcd, nil
}

func ContextMessenger(c context.Context) (*message.Messenger, error) {
	v := c.Value(contextKey)
	if v == nil {
		return nil, ErrInvalidContext
	}
	cv, ok := v.(*contextVal)
	if !ok {
		return nil, ErrInvalidContext
	}
	return cv.r.mm, nil
}

func ContextCoordinator(c context.Context) (*discovery.Coordinator, error) {
	v := c.Value(contextKey)
	if v == nil {
		return nil, ErrInvalidContext
	}
	cv, ok := v.(*contextVal)
	if !ok {
		return nil, ErrInvalidContext
	}
	return cv.r.co, nil
}
