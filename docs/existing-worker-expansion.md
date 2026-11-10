# Existing read-cache Worker expansion

This is an **opt-in, worker-only** path for the existing single-Master/single-Worker read-cache layout. It adds one Worker on a different prepared Kubernetes node. It does not adopt or patch Helm resources, change Master membership, migrate the source cluster, or relax ordinary CurvineCluster safety gates.

The code and isolated tests are implemented. **The sample dev deployment and its additional i5e node have not been modified or integration-tested by this change.** New-node preparation, source snapshot review, image approval and live acceptance remain deployment prerequisites.

## API and ownership

`CurvineWorkerExpansion` is a separate namespaced CR. Its binding pins:

- Existing Master and Worker StatefulSet names, UIDs and canonical spec hashes.
- Every referenced ConfigMap name, UID and content hash.
- Existing cluster ID, target Node name and UID, approved runtime image digest, and local data-volume names.
- Explicit `ReadCache` purpose. This is an administrative declaration of an existing read-cache deployment; the Operator does not infer it from a namespace name or prove its complete origin policy.

`spec.replicas` is the **total Worker count**, including the existing Worker. `1` performs preflight and writes only CR status; `2` requests one additional Worker. Admission rejects binding changes and decreases. The source Worker must remain at one replica. Only this 1→2 workflow is supported in this initial import contract.

Helm continues to own the original Master, Worker, Services and ConfigMaps. The Operator creates a separately named StatefulSet, headless Service, immutable configuration snapshots and a disk-identity guard ConfigMap. The new Pod keeps the reviewed source command, ports, resource requests, tolerations and NVMe guard; its image is pinned, hostname advertisement uses the node IP, and placement is pinned to the new node with anti-affinity against the source Worker.

A non-expiring Lease, keyed by source Worker UID, permits only one expansion CR to allocate an additional Worker for that source. Competing requests report `ReservationConflict`. Newly created resources deliberately have **no garbage-collecting ownerReference**: deleting the expansion CR retains the added Worker and its local data. The same-name replacement CR cannot adopt those resources or the old reservation. Retiring an expansion requires separately coordinated drain/cleanup; there is no automatic rollback, shrink, deletion or lease stealing.

## Supported import contract

The current adapter requires one ready Master and one ready Worker, host networking, Worker RPC/web ports 8997/19002, Master web port 9000, cluster.local Master DNS, one Worker container, one reviewed NVMe-check initContainer using the same source image, and local data volumes mounted by both containers. It preserves the source resource requests and tolerations. Target nodes must be Ready, uncordoned, correctly identified by hostname label/UID, distinct from the original Master/Worker nodes, and large enough for the Worker's requests; the scheduler determines remaining available capacity and host-port availability.

Every data hostPath must already be a `Directory` under `/mnt/`; `DirectoryOrCreate`, PVCs, unsupported volume sources, unpinned configuration references and Secret/config-map environment references are rejected by this initial adapter. The source runtime TOML must explicitly disable both format flags, identify the bound cluster and reference its existing Master. These checks complement, rather than replace, review of the hash-pinned source commands and NVMe guard. Init containers and their configuration are executable inputs: only snapshot an audited source deployment.

Immutable copies of the pinned configuration are mounted into the new Worker. Existing configuration is never rewritten. External source drift blocks subsequent reconciliation instead of silently changing the added Worker's template. No arbitrary pod-template overrides or rollout repairs are performed.

After the original NVMe guard succeeds, an additional Python guard checks dedicated writable NVMe mount points, distinct physical devices, and disk ownership. It claims only empty directories (apart from filesystem lost+found), writes/fsyncs a marker containing CR UID and cluster ID, and accepts matching markers on restart. Nonempty unclaimed disks, foreign/symlink/corrupt markers and shared devices fail closed. Interrupted incomplete marker writes require manual inspection; no disk is formatted or erased. The imported image must include Python 3, as required by this guard and the reviewed source guard.

## Application evidence

The imported Observer queries only `/api/overview` and `/api/workers` on the Master Pod IP read from Kubernetes, with timeouts, bounded strict JSON, no redirects and no ambient proxies. It verifies the Master identity and Active role before and after inventory, exact expected node-IP Worker identities, unique IDs/sessions, live status, fresh heartbeat, usable storage and protocol version 1. Unexpected/lost Workers or failed disks block validation.

Existing and added Pod imageIDs must match the explicitly approved repository digest. This supports the inspected runtime whose source commit was `unknown`, **without claiming that it was built from v0.5.4-alpha**. `ReleaseProvenanceVerified` remains Unknown. No quorum or global data-availability proof is inferred from an Active Master.

`status.phase` progresses through `PreflightReady`, `Progressing`, and `Registered`, or reports `Blocked` / `Unknown` with controlled reasons. `Registered` means both Workers have resource/runtime/registration evidence, not that new data has been placed. `DataPathVerified` stays Unknown until separately reviewed acceptance evidence exists; the controller does not run business reads or publish external test results as trusted status. Progress timestamps survive restarts, and a ten-minute stall condition is diagnostic only.

## Prepare without deploying

The offline renderer takes already-exported, non-public JSON snapshots. It neither reads kubeconfig nor contacts Kubernetes. Keep snapshot files private: existing ConfigMaps can contain sensitive values; the rendered CR contains hashes and references only.

```sh
make build
bin/expansion-plan \
  --master /private/path/master.json \
  --worker /private/path/worker.json \
  --configmaps /private/path/referenced-configmaps.json \
  --node /private/path/new-node.json \
  --name readcache-expansion \
  --cluster-id EXISTING_CLUSTER_ID \
  --image REGISTRY/curvine@sha256:APPROVED_DIGEST \
  > /private/path/expansion.json
```

The ConfigMap input is a Kubernetes ConfigMapList with exactly the maps referenced by the Worker. The Node must already have its prepared NVMe mounts; neither this command nor the Operator provisions machines or mounts/formats host disks. Output always has `replicas: 1`. Review it before any later deployment.

The optional `config/existing-expansion` overlay enables `--enable-existing-expansion=true`. Render an environment overlay into the existing cluster namespace and set a real Operator image. The extra ClusterRole grants only `get nodes`; the manager remains namespace-scoped and does not list/watch/mutate Nodes or read Secrets. Check the rendered ServiceAccount/RoleBinding/ClusterRoleBinding namespaces before deployment. Nothing is applied by building or rendering these files.

## Explicit acceptance after deployment authorization

`tests/e2e/existing_scale_out.py` is an executable acceptance tool, **not a read-only planning command**. It requires explicit kubeconfig/context/namespace, a current `PreflightReady` CR, a Python-capable consumer with a real read-only FUSE mount, one warm probe and separate immutable cold-object manifests. It patches only the specified expansion CR from 1 to 2, optionally restarts the named Operator Deployment, continuously verifies file length/SHA256, waits for current registration, and verifies read/write byte growth on the added Worker's port 19002. It leaves expanded resources in place on failure; it never rolls back or removes data.

```sh
python3 tests/e2e/existing_scale_out.py \
  --kubeconfig /private/path/test.kubeconfig --context EXPLICIT_CONTEXT \
  --namespace EXISTING_NAMESPACE --expansion readcache-expansion \
  --consumer EXISTING_TEST_CONSUMER --mount-path /data \
  --probe /private/path/warm-probe.json --manifest /private/path/cold-objects.json \
  --restart-operator curvine-operator --output /private/path/result.json
```

Each manifest item has `path`, `sizeBytes`, `sha256`. The existing bounded-manifest validation is reused. The caller needs CR get/patch, consumer exec and Worker pod proxy access, plus Deployment patch/get when restarting the Operator. These permissions are not added to the Operator. Reports exclude raw commands, configuration, Secret values and Pod logs. This tool's command flow is unit-tested with fake Kubernetes responses; actual two-node application/FUSE acceptance remains pending.

## Verification scope

Unit tests cover binding validation, immutable source preservation, preflight without allocation, stale intent/configuration fencing, duplicate-expansion reservation, restart convergence, placement/runtime rejection, application inventory validation, disk ownership, offline rendering, conservative status, and acceptance command scope. Envtest additionally verifies binding immutability, scale-in rejection and API defaults without a real kubelet or storage driver.

Real NVMe hardware, source-image Python availability/permissions, source guard behavior on the new node, admission policy, image pulling, networking, two-node data distribution and failure recovery still require the authorized deployment acceptance. Do not describe this code-only delivery as sample dev being expanded.

Local checks completed on 2026-11-10:

- `make generate`: generated CRD and DeepCopy successfully.
- `make check build`: Go race tests, six Python unit tests, vet, formatting and all four binaries passed.
- `make test-envtest`: existing control-plane suite passed (14.324s); new expansion admission/defaulting suite passed (7.314s), using isolated Kubernetes 1.36.0 API servers.
- `kubectl kustomize config/existing-expansion`: offline rendering passed.

No live-cluster resource or existing repository commit was changed by these checks.
