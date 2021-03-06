package controllers

import (
	"context"
	"strings"

	"github.com/rancher/fleet/pkg/controllers/bootstrap"
	"github.com/rancher/fleet/pkg/controllers/bundle"
	"github.com/rancher/fleet/pkg/controllers/cleanup"
	"github.com/rancher/fleet/pkg/controllers/cluster"
	"github.com/rancher/fleet/pkg/controllers/clustergroup"
	"github.com/rancher/fleet/pkg/controllers/clusterregistration"
	"github.com/rancher/fleet/pkg/controllers/clusterregistrationtoken"
	"github.com/rancher/fleet/pkg/controllers/config"
	"github.com/rancher/fleet/pkg/controllers/display"
	"github.com/rancher/fleet/pkg/controllers/git"
	"github.com/rancher/fleet/pkg/controllers/manageagent"
	"github.com/rancher/fleet/pkg/generated/controllers/fleet.cattle.io"
	fleetcontrollers "github.com/rancher/fleet/pkg/generated/controllers/fleet.cattle.io/v1alpha1"
	"github.com/rancher/fleet/pkg/manifest"
	"github.com/rancher/fleet/pkg/target"
	"github.com/rancher/gitjob/pkg/generated/controllers/gitjob.cattle.io"
	gitcontrollers "github.com/rancher/gitjob/pkg/generated/controllers/gitjob.cattle.io/v1"
	"github.com/rancher/wrangler/pkg/apply"
	"github.com/rancher/wrangler/pkg/generated/controllers/apps"
	appscontrollers "github.com/rancher/wrangler/pkg/generated/controllers/apps/v1"
	"github.com/rancher/wrangler/pkg/generated/controllers/core"
	corecontrollers "github.com/rancher/wrangler/pkg/generated/controllers/core/v1"
	"github.com/rancher/wrangler/pkg/generated/controllers/rbac"
	rbaccontrollers "github.com/rancher/wrangler/pkg/generated/controllers/rbac/v1"
	"github.com/rancher/wrangler/pkg/leader"
	"github.com/rancher/wrangler/pkg/ratelimit"
	"github.com/rancher/wrangler/pkg/start"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type appContext struct {
	fleetcontrollers.Interface

	K8s           kubernetes.Interface
	Core          corecontrollers.Interface
	Apps          appscontrollers.Interface
	RBAC          rbaccontrollers.Interface
	GitJob        gitcontrollers.Interface
	TargetManager *target.Manager
	Apply         apply.Apply
	ClientConfig  clientcmd.ClientConfig
	starters      []start.Starter
}

func (a *appContext) start(ctx context.Context) error {
	return start.All(ctx, 50, a.starters...)
}

func registrationNamespace(systemNamespace string) string {
	systemRegistrationNamespace := strings.ReplaceAll(systemNamespace, "-system", "-clusters-system")
	if systemRegistrationNamespace == systemNamespace {
		return systemNamespace + "-clusters-system"
	}
	return systemRegistrationNamespace
}

func Register(ctx context.Context, systemNamespace string, cfg clientcmd.ClientConfig) error {
	appCtx, err := newContext(cfg)
	if err != nil {
		return err
	}

	systemRegistrationNamespace := registrationNamespace(systemNamespace)

	if err := addData(systemNamespace, systemRegistrationNamespace, appCtx); err != nil {
		return err
	}

	// config should be registered first to ensure the global
	// config is available to all components
	if err := config.Register(ctx,
		systemNamespace,
		appCtx.Core.ConfigMap()); err != nil {
		return err
	}

	clusterregistration.Register(ctx,
		appCtx.Apply.WithCacheTypes(
			appCtx.Core.ServiceAccount(),
			appCtx.Core.Secret(),
			appCtx.RBAC.Role(),
			appCtx.RBAC.RoleBinding(),
			appCtx.RBAC.ClusterRole(),
			appCtx.RBAC.ClusterRoleBinding(),
			appCtx.ClusterRegistration(),
			appCtx.Cluster()),
		systemNamespace,
		systemRegistrationNamespace,
		appCtx.Core.ServiceAccount(),
		appCtx.Core.Secret(),
		appCtx.RBAC.Role(),
		appCtx.RBAC.RoleBinding(),
		appCtx.RBAC.ClusterRole(),
		appCtx.RBAC.ClusterRoleBinding(),
		appCtx.ClusterRegistration(),
		appCtx.Cluster().Cache(),
		appCtx.Cluster())

	cluster.Register(ctx,
		appCtx.BundleDeployment(),
		appCtx.ClusterGroup().Cache(),
		appCtx.Cluster(),
		appCtx.Core.Namespace(),
		appCtx.Apply.WithCacheTypes(
			appCtx.Core.Namespace()))

	cluster.RegisterImport(ctx,
		systemNamespace,
		appCtx.Core.Secret().Cache(),
		appCtx.Cluster(),
		appCtx.ClusterRegistrationToken())

	bundle.Register(ctx,
		appCtx.Apply,
		appCtx.TargetManager,
		appCtx.Bundle(),
		appCtx.Cluster(),
		appCtx.BundleDeployment())

	clustergroup.Register(ctx,
		appCtx.Cluster(),
		appCtx.ClusterGroup())

	clusterregistrationtoken.Register(ctx,
		systemNamespace,
		systemRegistrationNamespace,
		appCtx.Apply.WithCacheTypes(
			appCtx.Core.Secret(),
			appCtx.Core.ServiceAccount(),
			appCtx.RBAC.Role(),
			appCtx.RBAC.RoleBinding()),
		appCtx.ClusterRegistrationToken(),
		appCtx.Core.ServiceAccount(),
		appCtx.Core.Secret().Cache())

	cleanup.Register(ctx,
		appCtx.Apply.WithCacheTypes(
			appCtx.Core.Secret(),
			appCtx.Core.ServiceAccount(),
			appCtx.RBAC.Role(),
			appCtx.RBAC.RoleBinding(),
			appCtx.RBAC.ClusterRole(),
			appCtx.RBAC.ClusterRoleBinding(),
			appCtx.ClusterRegistrationToken(),
			appCtx.ClusterRegistration(),
			appCtx.ClusterGroup(),
			appCtx.Cluster(),
			appCtx.Core.Namespace()),
		appCtx.Core.Secret(),
		appCtx.Core.ServiceAccount(),
		appCtx.BundleDeployment(),
		appCtx.RBAC.Role(),
		appCtx.RBAC.RoleBinding(),
		appCtx.RBAC.ClusterRole(),
		appCtx.RBAC.ClusterRoleBinding(),
		appCtx.Core.Namespace())

	manageagent.Register(ctx,
		systemNamespace,
		appCtx.Apply,
		appCtx.Core.Namespace(),
		appCtx.Cluster(),
		appCtx.Bundle())

	git.Register(ctx,
		appCtx.Apply.WithCacheTypes(
			appCtx.RBAC.Role(),
			appCtx.RBAC.RoleBinding(),
			appCtx.GitJob.GitJob(),
			appCtx.Core.ConfigMap(),
			appCtx.Core.ServiceAccount()),
		appCtx.GitJob.GitJob(),
		appCtx.BundleDeployment(),
		appCtx.GitRepoRestriction().Cache(),
		appCtx.GitRepo())

	bootstrap.Register(ctx,
		systemNamespace,
		appCtx.Apply.WithCacheTypes(
			appCtx.GitRepo(),
			appCtx.Cluster(),
			appCtx.ClusterGroup(),
			appCtx.Core.Namespace(),
			appCtx.Core.Secret()),
		appCtx.ClientConfig,
		appCtx.Core.ServiceAccount().Cache(),
		appCtx.Core.Secret().Cache())

	display.Register(ctx,
		appCtx.Cluster(),
		appCtx.ClusterGroup(),
		appCtx.GitRepo(),
		appCtx.BundleDeployment(),
		appCtx.Bundle())

	leader.RunOrDie(ctx, systemNamespace, "fleet-controller-lock", appCtx.K8s, func(ctx context.Context) {
		if err := appCtx.start(ctx); err != nil {
			logrus.Fatal(err)
		}
	})

	return nil
}

func newContext(cfg clientcmd.ClientConfig) (*appContext, error) {
	client, err := cfg.ClientConfig()
	if err != nil {
		return nil, err
	}
	client.RateLimiter = ratelimit.None

	core, err := core.NewFactoryFromConfig(client)
	if err != nil {
		return nil, err
	}
	corev := core.Core().V1()

	fleet, err := fleet.NewFactoryFromConfig(client)
	if err != nil {
		return nil, err
	}
	fleetv := fleet.Fleet().V1alpha1()

	rbac, err := rbac.NewFactoryFromConfig(client)
	if err != nil {
		return nil, err
	}
	rbacv := rbac.Rbac().V1()

	apps, err := apps.NewFactoryFromConfig(client)
	if err != nil {
		return nil, err
	}
	appsv := apps.Apps().V1()

	git, err := gitjob.NewFactoryFromConfig(client)
	if err != nil {
		return nil, err
	}
	gitv := git.Gitjob().V1()

	apply, err := apply.NewForConfig(client)
	if err != nil {
		return nil, err
	}
	apply = apply.WithSetOwnerReference(false, false)

	k8s, err := kubernetes.NewForConfig(client)
	if err != nil {
		return nil, err
	}

	targetManager := target.New(
		fleetv.Cluster().Cache(),
		fleetv.ClusterGroup().Cache(),
		fleetv.Bundle().Cache(),
		fleetv.BundleNamespaceMapping().Cache(),
		corev.Namespace().Cache(),
		manifest.NewStore(fleetv.Content()),
		fleetv.BundleDeployment().Cache())

	return &appContext{
		K8s:           k8s,
		Apps:          appsv,
		Interface:     fleetv,
		Core:          corev,
		RBAC:          rbacv,
		Apply:         apply,
		GitJob:        gitv,
		TargetManager: targetManager,
		ClientConfig:  cfg,
		starters: []start.Starter{
			core,
			apps,
			fleet,
			rbac,
			git,
		},
	}, nil
}
