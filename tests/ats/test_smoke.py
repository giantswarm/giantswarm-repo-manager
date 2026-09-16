"""ATS smoke for the giantswarm-repo-manager chart.

app-test-suite (>= 1.0) installs the packaged chart on the job's kind cluster
with `helm upgrade --install --wait` (tests/test-values.yaml, namespace from
.ats/main.yaml) and then runs this file with `pytest -m smoke`. The smoke is
the chart-level install check: the cluster is reachable and the chart's
Deployments are ready — the server with the image the branch build pushed, and
the Valkey subchart the inventory lives in.
"""

import logging
import os
from typing import List

import pykube
import pytest
from pytest_helm_charts.clusters import Cluster
from pytest_helm_charts.k8s.deployment import wait_for_deployments_to_run

logger = logging.getLogger(__name__)

NAMESPACE = os.environ.get("ATS_RELEASE_NAMESPACE", "giantswarm-repo-manager")
DEPLOYMENTS = ["giantswarm-repo-manager", "giantswarm-repo-manager-valkey"]
TIMEOUT = 300


@pytest.mark.smoke
def test_api_working(kube_cluster: Cluster) -> None:
    """The kind cluster ATS runs against is reachable."""
    assert kube_cluster.kube_client is not None
    assert len(pykube.Node.objects(kube_cluster.kube_client)) >= 1


@pytest.mark.smoke
@pytest.mark.upgrade
def test_deployments_ready(kube_cluster: Cluster) -> None:
    """The server and its Valkey reach their desired replica counts."""
    deployments: List[pykube.Deployment] = wait_for_deployments_to_run(
        kube_cluster.kube_client, DEPLOYMENTS, NAMESPACE, TIMEOUT
    )
    assert len(deployments) == len(DEPLOYMENTS)
    for d in deployments:
        assert int(d.obj["status"].get("readyReplicas", 0)) == int(d.obj["spec"]["replicas"])
        logger.info("Deployment %s/%s is ready (%s replicas)", NAMESPACE, d.name, d.obj["spec"]["replicas"])
