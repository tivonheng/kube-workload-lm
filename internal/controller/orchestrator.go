package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/lifecycle"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/policy"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/workload"
)

const (
	DefaultReconcileInterval = 30 * time.Second
	DefaultWatchRetryDelay   = time.Second
)

var (
	ErrInvalidController = errors.New("controller orchestration dependencies must be configured")
	ErrNoPolicySnapshot  = errors.New("no valid policy snapshot is available")
	ErrPolicyConflict    = errors.New("multiple highest-priority policies match workload")
)

type PolicyProvider interface {
	Current() (policy.PolicySet, bool)
}

type TransitionReconciler interface {
	Reconcile(context.Context, workload.Workload, policy.Policy) (lifecycle.Decision, error)
}

type HPAWatcher interface {
	Watch(context.Context) (workload.HPAWatchStream, error)
}

type ErrorHandler func(error)

// ReconcileObservation contains only bounded outcome fields plus workload
// identity for structured logs. Metrics observers must never use identity as labels.
type ReconcileObservation struct {
	Workload workload.Workload
	Policy   string
	Reason   string
	Result   string
	Decision lifecycle.Decision
	Err      error
}

// Observer receives controller lifecycle and outcome events without coupling the
// controller core to a logging or metrics implementation.
type Observer interface {
	ControllerStarted()
	ControllerStopped()
	ObserveReconcile(ReconcileObservation)
	ObservePolicyEvent(policy.Event)
	ObserveError(string, error)
}

type noopObserver struct{}

func (noopObserver) ControllerStarted()                    {}
func (noopObserver) ControllerStopped()                    {}
func (noopObserver) ObserveReconcile(ReconcileObservation) {}
func (noopObserver) ObservePolicyEvent(policy.Event)       {}
func (noopObserver) ObserveError(string, error)            {}

type ControllerOptions struct {
	ReconcileInterval time.Duration
	WatchRetryDelay   time.Duration
	HandleError       ErrorHandler
	Observer          Observer
}

type Controller struct {
	workloads   workload.WorkloadGateway
	hpa         HPAWatcher
	policies    PolicyProvider
	policyWatch policy.Watcher
	transition  TransitionReconciler
	interval    time.Duration
	retryDelay  time.Duration
	handleError ErrorHandler
	observer    Observer
	newTicker   func(time.Duration) reconcileTicker
	sleep       func(context.Context, time.Duration) error
}

type reconcileTicker interface {
	Chan() <-chan time.Time
	Stop()
}

type realTicker struct{ ticker *time.Ticker }

func (ticker realTicker) Chan() <-chan time.Time { return ticker.ticker.C }
func (ticker realTicker) Stop()                  { ticker.ticker.Stop() }

type reconcileRequest struct {
	workload *workload.Workload
	full     bool
}

func NewController(inventory workload.WorkloadGateway, hpa HPAWatcher, policies PolicyProvider, policyWatch policy.Watcher, transition TransitionReconciler, options ControllerOptions) (*Controller, error) {
	if inventory == nil || hpa == nil || policies == nil || policyWatch == nil || transition == nil {
		return nil, ErrInvalidController
	}
	interval := options.ReconcileInterval
	if interval <= 0 {
		interval = DefaultReconcileInterval
	}
	retryDelay := options.WatchRetryDelay
	if retryDelay <= 0 {
		retryDelay = DefaultWatchRetryDelay
	}
	handleError := options.HandleError
	if handleError == nil {
		handleError = func(error) {}
	}
	observer := options.Observer
	if observer == nil {
		observer = noopObserver{}
	}
	return &Controller{
		workloads: inventory, hpa: hpa, policies: policies, policyWatch: policyWatch,
		transition: transition, interval: interval, retryDelay: retryDelay, handleError: handleError,
		observer:  observer,
		newTicker: func(interval time.Duration) reconcileTicker { return realTicker{time.NewTicker(interval)} },
		sleep:     sleepWithContext,
	}, nil
}

// Run starts all resource watches, performs an initial full reconciliation,
// and then serializes event-driven and periodic reconciliations. Cancellation
// stops every watch and waits for its supervisor before returning.
func (controller *Controller) Run(ctx context.Context) error {
	controller.observer.ControllerStarted()
	defer controller.observer.ControllerStopped()
	runCtx, cancel := context.WithCancel(ctx)
	requests := make(chan reconcileRequest)
	var supervisors sync.WaitGroup
	supervisors.Add(3)
	go func() { defer supervisors.Done(); controller.superviseWorkloads(runCtx, requests) }()
	go func() { defer supervisors.Done(); controller.superviseHPA(runCtx, requests) }()
	go func() { defer supervisors.Done(); controller.supervisePolicies(runCtx, requests) }()
	defer func() {
		cancel()
		supervisors.Wait()
	}()

	ticker := controller.newTicker(controller.interval)
	defer ticker.Stop()
	controller.reconcileAll(runCtx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.Chan():
			controller.reconcileAll(runCtx)
		case request := <-requests:
			if request.full {
				controller.reconcileAll(runCtx)
			} else if request.workload != nil {
				controller.reconcileOne(runCtx, *request.workload)
			}
		}
	}
}

func (controller *Controller) reconcileAll(ctx context.Context) {
	for _, kind := range []workload.Kind{workload.KindDeployment, workload.KindStatefulSet} {
		items, err := controller.workloads.List(ctx, kind)
		if err != nil {
			controller.handleError(fmt.Errorf("list %s workloads: %w", kind, err))
			continue
		}
		for _, current := range items {
			if err := ctx.Err(); err != nil {
				return
			}
			controller.reconcileOne(ctx, current)
		}
	}
}

func (controller *Controller) reconcileOne(ctx context.Context, current workload.Workload) {
	set, ok := controller.policies.Current()
	if !ok {
		err := fmt.Errorf("reconcile %s: %w", current.Key(), ErrNoPolicySnapshot)
		controller.observer.ObserveReconcile(ReconcileObservation{Workload: current, Reason: "no-policy-snapshot", Result: "error", Err: err})
		controller.handleError(err)
		return
	}
	matched := policy.Match(set, current.Kind, current.Name, current.Labels)
	if matched.Conflict() {
		err := fmt.Errorf("reconcile %s: %w: %v", current.Key(), ErrPolicyConflict, matched.ConflictNames)
		controller.observer.ObserveReconcile(ReconcileObservation{Workload: current, Reason: "policy-conflict", Result: "error", Err: err})
		controller.handleError(err)
		return
	}
	if matched.Policy == nil {
		controller.observer.ObserveReconcile(ReconcileObservation{Workload: current, Reason: "no-matching-policy", Result: "skipped"})
		return
	}
	decision, err := controller.transition.Reconcile(ctx, current, *matched.Policy)
	observation := ReconcileObservation{Workload: current, Policy: matched.Policy.Name, Reason: decision.Reason, Decision: decision, Result: "success", Err: err}
	if err != nil {
		observation.Result = "error"
		if observation.Reason == "" {
			observation.Reason = "transition-error"
		}
		controller.observer.ObserveReconcile(observation)
		controller.handleError(fmt.Errorf("reconcile %s: %w", current.Key(), err))
		return
	}
	controller.observer.ObserveReconcile(observation)
}

func (controller *Controller) superviseWorkloads(ctx context.Context, requests chan<- reconcileRequest) {
	for ctx.Err() == nil {
		stream, err := controller.workloads.Watch(ctx)
		if err != nil {
			controller.handleError(fmt.Errorf("start workload watch: %w", err))
			if !controller.retry(ctx) {
				return
			}
			continue
		}
		restart := false
		for !restart {
			select {
			case <-ctx.Done():
				stream.Stop()
				return
			case event, ok := <-stream.ResultChan():
				if !ok {
					controller.handleError(workload.ErrWatchDisconnected)
					restart = true
					break
				}
				switch event.Type {
				case workload.EventAdded, workload.EventModified:
					if event.Workload != nil && !sendRequest(ctx, requests, reconcileRequest{workload: event.Workload}) {
						stream.Stop()
						return
					}
				case workload.EventDeleted:
					if !sendRequest(ctx, requests, reconcileRequest{full: true}) {
						stream.Stop()
						return
					}
				case workload.EventError, workload.EventDisconnected:
					controller.handleError(event.Err)
					restart = true
				}
			}
		}
		stream.Stop()
		if !controller.retry(ctx) {
			return
		}
	}
}

func (controller *Controller) superviseHPA(ctx context.Context, requests chan<- reconcileRequest) {
	for ctx.Err() == nil {
		stream, err := controller.hpa.Watch(ctx)
		if err != nil {
			controller.handleError(fmt.Errorf("start HPA watch: %w", err))
			if !controller.retry(ctx) {
				return
			}
			continue
		}
		restart := false
		for !restart {
			select {
			case <-ctx.Done():
				stream.Stop()
				return
			case event, ok := <-stream.ResultChan():
				if !ok {
					controller.handleError(workload.ErrHPAWatchDisconnected)
					restart = true
					break
				}
				if event.Type == workload.EventError || event.Type == workload.EventDisconnected {
					controller.handleError(event.Err)
					restart = true
				} else if !sendRequest(ctx, requests, reconcileRequest{full: true}) {
					stream.Stop()
					return
				}
			}
		}
		stream.Stop()
		if !controller.retry(ctx) {
			return
		}
	}
}

func (controller *Controller) supervisePolicies(ctx context.Context, requests chan<- reconcileRequest) {
	for ctx.Err() == nil {
		stream, err := controller.policyWatch.Watch(ctx)
		if err != nil {
			controller.handleError(fmt.Errorf("start policy watch: %w", err))
			if !controller.retry(ctx) {
				return
			}
			continue
		}
		restart := false
		for !restart {
			select {
			case <-ctx.Done():
				stream.Stop()
				return
			case event, ok := <-stream.ResultChan():
				if !ok {
					controller.observer.ObservePolicyEvent(policy.Event{Type: policy.EventDisconnected, Err: policy.ErrPolicyWatchDisconnected})
					controller.handleError(policy.ErrPolicyWatchDisconnected)
					restart = true
					break
				}
				controller.observer.ObservePolicyEvent(event)
				switch event.Type {
				case policy.EventReloaded, policy.EventDeleted:
					if !sendRequest(ctx, requests, reconcileRequest{full: true}) {
						stream.Stop()
						return
					}
				case policy.EventReloadRejected:
					controller.handleError(event.Err)
				case policy.EventError, policy.EventDisconnected:
					controller.handleError(event.Err)
					restart = true
				}
			}
		}
		stream.Stop()
		if !controller.retry(ctx) {
			return
		}
	}
}

func sendRequest(ctx context.Context, requests chan<- reconcileRequest, request reconcileRequest) bool {
	select {
	case requests <- request:
		return true
	case <-ctx.Done():
		return false
	}
}

func (controller *Controller) retry(ctx context.Context) bool {
	return controller.sleep(ctx, controller.retryDelay) == nil
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
