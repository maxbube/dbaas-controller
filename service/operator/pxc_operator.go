// dbaas-controller
// Copyright (C) 2020 Percona LLC
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package operator

import (
	"context"

	controllerv1beta1 "github.com/percona-platform/dbaas-api/gen/controller"
	"golang.org/x/text/message"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/percona-platform/dbaas-controller/service/k8sclient"
)

const pxcOperatorDeploymentName = "percona-xtradb-cluster-operator"

type PXCOperatorService struct {
	p                    *message.Printer
	manifestsURLTemplate string
}

// NewPXCOperatorService returns new PXCOperatorService instance.
func NewPXCOperatorService(p *message.Printer, url string) *PXCOperatorService {
	return &PXCOperatorService{p: p, manifestsURLTemplate: url}
}

func (x PXCOperatorService) InstallPXCOperator(ctx context.Context, req *controllerv1beta1.InstallPXCOperatorRequest) (*controllerv1beta1.InstallPXCOperatorResponse, error) {
	client, err := k8sclient.New(ctx, req.KubeAuth.Kubeconfig)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	defer client.Cleanup() //nolint:errcheck

	// Try to get operator versions to see if we should upgrade or install.
	operators, err := client.CheckOperators(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// NOTE: This does not handle corner case when user has deployed database clusters and operator is no longer installed.
	if operators.PXCOperatorVersion != "" {
		err = client.UpdateOperator(ctx, req.Version, pxcOperatorDeploymentName, x.manifestsURLTemplate)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		err = client.PatchAllPXCClusters(ctx, operators.PXCOperatorVersion, req.Version)
		if err != nil {
			return nil, err
		}

		return new(controllerv1beta1.InstallPXCOperatorResponse), nil
	}

	err = client.ApplyOperator(ctx, req.Version, x.manifestsURLTemplate)
	if err != nil {
		return nil, err
	}

	return new(controllerv1beta1.InstallPXCOperatorResponse), nil
}
