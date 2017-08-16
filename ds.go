package kail

import (
	"context"

	logutil "github.com/boz/go-logutil"
	"github.com/boz/kcache/filter"
	"github.com/boz/kcache/join"
	"github.com/boz/kcache/nsname"
	"github.com/boz/kcache/types/daemonset"
	"github.com/boz/kcache/types/deployment"
	"github.com/boz/kcache/types/node"
	"github.com/boz/kcache/types/pod"
	"github.com/boz/kcache/types/replicaset"
	"github.com/boz/kcache/types/replicationcontroller"
	"github.com/boz/kcache/types/service"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

type DSBuilder interface {
	WithNamespace(name ...string) DSBuilder
	WithPods(id ...nsname.NSName) DSBuilder
	WithSelectors(selectors ...labels.Selector) DSBuilder
	WithService(id ...nsname.NSName) DSBuilder
	WithNode(name ...string) DSBuilder
	WithRC(id ...nsname.NSName) DSBuilder
	WithRS(id ...nsname.NSName) DSBuilder
	WithDS(id ...nsname.NSName) DSBuilder
	WithDeployment(id ...nsname.NSName) DSBuilder

	Create(ctx context.Context, cs kubernetes.Interface) (DS, error)
}

type DS interface {
	Pods() pod.Controller
	Ready() <-chan struct{}
	Done() <-chan struct{}
	Shutdown()
}

type dsBuilder struct {
	namespaces  []string
	pods        []nsname.NSName
	selectors   []labels.Selector
	services    []nsname.NSName
	nodes       []string
	rcs         []nsname.NSName
	rss         []nsname.NSName
	dss         []nsname.NSName
	deployments []nsname.NSName
}

func NewDSBuilder() DSBuilder {
	return &dsBuilder{}
}

func (b *dsBuilder) WithNamespace(name ...string) DSBuilder {
	b.namespaces = append(b.namespaces, name...)
	return b
}

func (b *dsBuilder) WithPods(id ...nsname.NSName) DSBuilder {
	b.pods = append(b.pods, id...)
	return b
}

func (b *dsBuilder) WithSelectors(selectors ...labels.Selector) DSBuilder {
	b.selectors = append(b.selectors, selectors...)
	return b
}

func (b *dsBuilder) WithService(id ...nsname.NSName) DSBuilder {
	b.services = append(b.services, id...)
	return b
}

func (b *dsBuilder) WithNode(name ...string) DSBuilder {
	b.nodes = append(b.nodes, name...)
	return b
}

func (b *dsBuilder) WithRC(id ...nsname.NSName) DSBuilder {
	b.rcs = append(b.rcs, id...)
	return b
}

func (b *dsBuilder) WithRS(id ...nsname.NSName) DSBuilder {
	b.rss = append(b.rss, id...)
	return b
}

func (b *dsBuilder) WithDS(id ...nsname.NSName) DSBuilder {
	b.dss = append(b.dss, id...)
	return b
}

func (b *dsBuilder) WithDeployment(id ...nsname.NSName) DSBuilder {
	b.deployments = append(b.deployments, id...)
	return b
}

func (b *dsBuilder) Create(ctx context.Context, cs kubernetes.Interface) (DS, error) {
	log := logutil.FromContextOrDefault(ctx)

	ds := &datastore{
		readych: make(chan struct{}),
		donech:  make(chan struct{}),
	}

	base, err := pod.NewController(ctx, log, cs, "")
	if err != nil {
		return nil, log.Err(err, "base pod controller")
	}

	ds.podBase = base
	ds.pods, err = base.CloneWithFilter(filter.Null())
	if err != nil {
		ds.closeAll()
		return nil, log.Err(err, "null filter")
	}

	if sz := len(b.namespaces); sz > 0 {
		ids := make([]nsname.NSName, 0, sz)
		for _, ns := range b.namespaces {
			ids = append(ids, nsname.New(ns, ""))
		}

		ds.pods, err = ds.pods.CloneWithFilter(filter.NSName(ids...))
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "namespace filter")
		}
	}

	if len(b.pods) != 0 {
		ds.pods, err = ds.pods.CloneWithFilter(filter.NSName(b.pods...))
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "pods filter")
		}
	}

	if len(b.selectors) != 0 {
		filters := make([]filter.Filter, 0, len(b.selectors))
		for _, selector := range b.selectors {
			filters = append(filters, filter.Selector(selector))
		}
		ds.pods, err = ds.pods.CloneWithFilter(filter.And(filters...))
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "labels filter")
		}
	}

	if len(b.nodes) != 0 {
		ds.pods, err = ds.pods.CloneWithFilter(pod.NodeFilter(b.nodes...))
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "node filter")
		}
	}

	if len(b.services) != 0 {
		ds.servicesBase, err = service.NewController(ctx, log, cs, "")
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "service base controller")
		}

		ds.services, err = ds.servicesBase.CloneWithFilter(filter.NSName(b.services...))
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "service controller")
		}

		ds.pods, err = join.ServicePods(ctx, ds.services, ds.pods)
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "service join")
		}
	}

	if len(b.rcs) != 0 {
		ds.rcsBase, err = replicationcontroller.NewController(ctx, log, cs, "")
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "rc base controller")
		}

		ds.rcs, err = ds.rcsBase.CloneWithFilter(filter.NSName(b.rcs...))
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "rc controller")
		}

		ds.pods, err = join.RCPods(ctx, ds.rcs, ds.pods)
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "rc join")
		}
	}

	if len(b.rss) != 0 {
		ds.rssBase, err = replicaset.NewController(ctx, log, cs, "")
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "rs base controller")
		}

		ds.rss, err = ds.rssBase.CloneWithFilter(filter.NSName(b.rss...))
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "rs controller")
		}

		ds.pods, err = join.RSPods(ctx, ds.rss, ds.pods)
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "rs join")
		}
	}

	if len(b.dss) != 0 {
		ds.dssBase, err = daemonset.NewController(ctx, log, cs, "")
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "ds base controller")
		}

		ds.dss, err = ds.dssBase.CloneWithFilter(filter.NSName(b.dss...))
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "ds controller")
		}

		ds.pods, err = join.DaemonSetPods(ctx, ds.dss, ds.pods)
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "ds join")
		}
	}

	if len(b.deployments) != 0 {
		ds.deploymentsBase, err = deployment.NewController(ctx, log, cs, "")
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "deployment base controller")
		}

		ds.deployments, err = ds.deploymentsBase.CloneWithFilter(filter.NSName(b.deployments...))
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "deployment controller")
		}

		ds.pods, err = join.DeploymentPods(ctx, ds.deployments, ds.pods)
		if err != nil {
			ds.closeAll()
			return nil, log.Err(err, "deployment join")
		}
	}

	go ds.waitReadyAll()
	go ds.waitDoneAll()

	return ds, nil
}

type datastore struct {
	podBase         pod.Controller
	servicesBase    service.Controller
	nodesBase       node.Controller
	rcsBase         replicationcontroller.Controller
	rssBase         replicaset.Controller
	dssBase         daemonset.Controller
	deploymentsBase deployment.Controller

	pods        pod.Controller
	services    service.Controller
	nodes       node.Controller
	rcs         replicationcontroller.Controller
	rss         replicaset.Controller
	dss         daemonset.Controller
	deployments deployment.Controller

	readych chan struct{}
	donech  chan struct{}
}

func (ds *datastore) Pods() pod.Controller {
	return ds.pods
}

func (ds *datastore) Ready() <-chan struct{} {
	return ds.readych
}

func (ds *datastore) Done() <-chan struct{} {
	return ds.donech
}

func (ds *datastore) Shutdown() {
	ds.closeAll()
}

func (ds *datastore) waitReadyAll() {
	for _, c := range ds.controllers() {
		select {
		case <-c.Done():
			return
		case <-c.Ready():
		}
	}
	close(ds.readych)
}

func (ds *datastore) closeAll() {
	for _, c := range ds.controllers() {
		c.Close()
	}
}

func (ds *datastore) waitDoneAll() {
	defer close(ds.donech)
	for _, c := range ds.controllers() {
		<-c.Done()
	}
}

func (ds *datastore) controllers() []cacheController {

	potential := []cacheController{
		ds.podBase,
		ds.servicesBase,
		ds.nodesBase,
		ds.rcsBase,
		ds.rssBase,
		ds.dssBase,
		ds.deploymentsBase,
		ds.pods,
		ds.services,
		ds.nodes,
		ds.rcs,
		ds.rss,
		ds.dss,
		ds.deployments,
	}

	var existing []cacheController
	for _, c := range potential {
		if c != nil {
			existing = append(existing, c)
		}
	}
	return existing
}

type cacheController interface {
	Close()
	Done() <-chan struct{}
	Ready() <-chan struct{}
}
