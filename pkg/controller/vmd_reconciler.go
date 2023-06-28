package controller

import (
	"context"
	"fmt"
	"github.com/deckhouse/virtualization-controller/pkg/util"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"

	virtv2 "github.com/deckhouse/virtualization-controller/api/v2alpha1"
	"github.com/deckhouse/virtualization-controller/pkg/sdk/framework/two_phase_reconciler"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type VMDReconciler struct{}

func (r *VMDReconciler) SetupController(ctx context.Context, mgr manager.Manager, ctr controller.Controller) error {
	if err := ctr.Watch(&source.Kind{Type: &virtv2.VirtualMachineDisk{}}, &handler.EnqueueRequestForObject{},
		predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool { return true },
			DeleteFunc: func(e event.DeleteEvent) bool { return true },
			UpdateFunc: func(e event.UpdateEvent) bool { return true },
		},
	); err != nil {
		return err
	}
	if err := ctr.Watch(&source.Kind{Type: &cdiv1.DataVolume{}}, &handler.EnqueueRequestForOwner{
		OwnerType:    &virtv2.VirtualMachineDisk{},
		IsController: true,
	}); err != nil {
		return err
	}

	return nil
}

func (r *VMDReconciler) Sync(ctx context.Context, req reconcile.Request, state *VMDReconcilerState, opts two_phase_reconciler.ReconcilerOptions) error {
	if util.IsEmpty(state.PersistentVolumeClaimName) {
		state.PersistentVolumeClaimName = types.NamespacedName{
			Name:      fmt.Sprintf("virtual-machine-disk-%s", uuid.NewUUID()),
			Namespace: req.Namespace,
		}
		opts.Log.Info("Generated PVC name", "pvcname", state.PersistentVolumeClaimName.Name)
		dv := NewDVFromVirtualMachineDisk(state.PersistentVolumeClaimName, state.VMD.Read())
		if err := opts.Client.Create(ctx, dv); err != nil {
			return fmt.Errorf("unable to create DV %q: %w", dv.Name, err)
		}

		state.DV = dv
		opts.Log.Info("Created new DV", "name", dv.Name, "dv", dv)
	}

	return nil
}

func (r *VMDReconciler) UpdateStatus(ctx context.Context, req reconcile.Request, state *VMDReconcilerState, opts two_phase_reconciler.ReconcilerOptions) error {
	opts.Log.Info("Update Status", "pvcname", state.PersistentVolumeClaimName.Name)

	if state.VMD.Read().Status.PersistentVolumeClaimName == "" {
		state.VMD.Write().Status.PersistentVolumeClaimName = state.PersistentVolumeClaimName.Name
	}

	if state.VMD.Read().Status.Size == "" {
		if state.PVC != nil {
			state.VMD.Write().Status.Size = util.GetPointer(state.PVC.Status.Capacity[corev1.ResourceRequestsStorage]).String()
		}
	}

	switch state.VMD.Read().Status.Phase {
	case "", virtv2.DiskPending:
		if state.DV != nil {
			progress := virtv2.DiskProgress(state.DV.Status.Progress)
			if progress == "" {
				progress = "N/A"
			}
			state.VMD.Write().Status.Progress = progress
			state.VMD.Write().Status.Phase = MapDataVolumePhaseToVMDPhase(state.DV.Status.Phase)
		} else {
			state.VMD.Write().Status.Phase = virtv2.DiskPending
		}
	case virtv2.DiskWaitForUserUpload:
	// TODO
	case virtv2.DiskProvisioning:
		if state.DV != nil {
			state.VMD.Write().Status.Progress = virtv2.DiskProgress(state.DV.Status.Progress)
			state.VMD.Write().Status.Phase = MapDataVolumePhaseToVMDPhase(state.DV.Status.Phase)
		} else {
			opts.Log.Info("Lost DataVolume, will skip update status")
		}
	case virtv2.DiskReady:
		// TODO
	case virtv2.DiskFailed:
		// TODO
	case virtv2.DiskNotReady:
		// TODO
	case virtv2.DiskPVCLost:
		// TODO
	}
	return nil
}

func NewDVFromVirtualMachineDisk(name types.NamespacedName, vmd *virtv2.VirtualMachineDisk) *cdiv1.DataVolume {
	labels := map[string]string{}
	annotations := map[string]string{
		"cdi.kubevirt.io/storage.deleteAfterCompletion":    "false",
		"cdi.kubevirt.io/storage.bind.immediate.requested": "true",
	}

	// FIXME: resource.Quantity should be defined directly in the spec struct (see PVC impl. for details)
	pvcSize, err := resource.ParseQuantity(vmd.Spec.PersistentVolumeClaim.Size)
	if err != nil {
		panic(err.Error())
	}

	res := &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   name.Namespace,
			Name:        name.Name,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: &cdiv1.DataVolumeSource{},
			PVC: &corev1.PersistentVolumeClaimSpec{
				StorageClassName: &vmd.Spec.PersistentVolumeClaim.StorageClassName,
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, // TODO: ensure this mode is appropriate
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: pvcSize,
					},
				},
			},
		},
	}

	if vmd.Spec.DataSource.HTTP != nil {
		res.Spec.Source.HTTP = &cdiv1.DataVolumeSourceHTTP{
			URL: vmd.Spec.DataSource.HTTP.URL,
		}
	}

	res.OwnerReferences = []metav1.OwnerReference{
		*metav1.NewControllerRef(vmd, schema.GroupVersionKind{
			Group:   virtv2.SchemeGroupVersion.Group,
			Version: virtv2.SchemeGroupVersion.Version,
			Kind:    "VirtualMachineDisk",
		}),
	}

	return res
}

func MapDataVolumePhaseToVMDPhase(phase cdiv1.DataVolumePhase) virtv2.DiskPhase {
	switch phase {
	case cdiv1.PhaseUnset, cdiv1.Unknown, cdiv1.Pending:
		return virtv2.DiskPending
	case cdiv1.WaitForFirstConsumer, cdiv1.PVCBound,
		cdiv1.ImportScheduled, cdiv1.CloneScheduled, cdiv1.UploadScheduled,
		cdiv1.ImportInProgress, cdiv1.CloneInProgress,
		cdiv1.SnapshotForSmartCloneInProgress, cdiv1.SmartClonePVCInProgress,
		cdiv1.CSICloneInProgress,
		cdiv1.CloneFromSnapshotSourceInProgress,
		cdiv1.Paused:
		return virtv2.DiskProvisioning
	case cdiv1.Succeeded:
		return virtv2.DiskReady
	case cdiv1.Failed:
		return virtv2.DiskFailed
	default:
		panic(fmt.Sprintf("unexpected DataVolume phase %q, please report a bug", phase))
	}
}
