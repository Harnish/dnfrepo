# Moving an existing registry to dnfrepo

For a standalone `registry:2` deployment (stock image, no auth) whose data directory you want dnfrepo to serve. Existing images carry over: dnfrepo uses the same on-disk layout.

Assumes: registry hostname `registry.example.org`, data in `/data/registry`, dnfrepo deployed under `/path/to/dnfrepo/`.

## Before you start

1. **Back up the data directory** (`/data/registry`), or at least know you can restore it. dnfrepo can delete (GC) and rewrite it.
2. **Make it writable by the dnfrepo user.** The old registry usually ran as root, so the tree is root-owned while dnfrepo runs as a normal user (uid 1000 in the example manifest, 10001 in the Helm chart). Run once on the node, as the owner dnfrepo uses:

   ```
   sudo chown -R 1000:1000 /data/registry
   ```
3. Build and push the dnfrepo image that contains the registry (this version or later).

## Cutover (a few minutes of registry downtime)

1. **Stop the old registry** so two writers never share the directory: scale the old Deployment to 0 (`kubectl scale deploy/registry --replicas=0`). Keep its manifests; they are your rollback.
2. **Give dnfrepo the data.** In dnfrepo's Deployment add a volume and mount for the registry directory and these env vars:

   ```yaml
   env:
     - { name: REGISTRY_DIR,  value: /registry }
     - { name: REGISTRY_HOST, value: registry.example.org }
   volumeMounts:
     - { name: registry, mountPath: /registry }
   volumes:
     - name: registry
       hostPath: { path: /data/registry, type: Directory }
   ```

   (Helm: `registry.enabled=true`, `registry.host=...`, `registry.hostPath=/data/registry`.)
3. **Route the hostname to dnfrepo.** Point the registry hostname's Ingress rule at the dnfrepo Service (port 80), and remove the old registry's Ingress/Service so the name is not claimed twice. Keep the same TLS secret.
4. **Apply and verify:**

   ```
   kubectl apply -f deployment.yaml -f ingress.yaml
   curl -s https://registry.example.org/v2/_catalog     # anonymous: {"repositories":[]} until images are Listed
   docker pull registry.example.org/<an existing image>:<tag>   # anonymous pull still works
   ```
5. **Create a push token** (admin UI, *Push tokens*) and log in wherever you push from:

   ```
   docker login registry.example.org -u <token name>     # token as the password
   ```

   Anything that pushed anonymously before (build scripts, CI) must do this once. Cluster nodes that only pull are unaffected: existing images are public until you change them.
6. In the admin UI, tick **Listed** on the images that should appear on `/browse/registry`, and set `users`/`ips` on anything private.

## Rollback

Scale dnfrepo's registry out (remove `REGISTRY_DIR` and the mount) and scale the old registry back up; its manifests are untouched and the data directory is unchanged apart from files dnfrepo wrote or GC removed. If you ran `chown` (step 2) and the old registry runs as root, root can still read and write it.

## Notes

- Retire any separate registry UI deployment once dnfrepo's pages cover it.
- Run garbage collection from the admin UI only when you want to reclaim space; it refuses pushes while it runs.
