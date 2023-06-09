package controller

import (
	"context"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/deckhouse/virtualization-controller/pkg/sdk/framework/two_phase_reconciler"
)

const (
	vmdControllerName = "vmd-controller"
)

func NewVMDController(ctx context.Context, mgr manager.Manager, log logr.Logger) (controller.Controller, error) {
	reconciler := &VMDReconciler{}
	reconcilerCore := two_phase_reconciler.NewReconcilerCore[*VMDReconcilerState](
		reconciler,
		NewVMDReconcilerState,
		two_phase_reconciler.ReconcilerOptions{
			Client:   mgr.GetClient(),
			Cache:    mgr.GetCache(),
			Recorder: mgr.GetEventRecorderFor(vmdControllerName),
			Scheme:   mgr.GetScheme(),
			Log:      log.WithName(vmdControllerName),
		})

	c, err := controller.New(vmdControllerName, mgr, controller.Options{Reconciler: reconcilerCore})
	if err != nil {
		return nil, err
	}
	if err := reconciler.SetupController(ctx, mgr, c); err != nil {
		return nil, err
	}
	log.Info("Initialized VirtualMachineDisk controller")
	return c, nil
}
