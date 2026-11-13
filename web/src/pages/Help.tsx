const o = location.origin

const terms: [string, string][] = [
  ['Repo', `One dnf repository, served at ${o}/r/<slug>/. The slug is its short URL name (lowercase letters, digits, dashes).`],
  ['Pipeline / stage', 'An ordered chain of repos, for example dev → test → prod. Each repo in the chain is a stage. Only the first stage takes uploads and sources.'],
  ['Promote', 'Copy chosen rpm files from a stage into the next one. All-or-nothing: if any file is a problem, none move. This is the only way a package enters a later stage.'],
  ['Source', 'An automatic feed into a first-stage repo: the latest rpm from a GitHub project (owner/name) or from Plex Media Server. Checked daily or on demand with Run now.'],
  ['Access mode', 'Exactly one per repo. public: anyone. ips: only listed IPs/CIDR ranges. users: HTTP Basic auth with a repo user and token. It covers rpms, metadata, browse page, feed and .repo file.'],
  ['Repo user / token', 'A name plus a secret token that dnf clients use to pull from users-mode repos. The token is shown once when created; revoke the user to cut it off.'],
  ['.repo file', 'The client config that tells dnf where the repo is and which GPG key to use. Download it from the repo page or from the Repo users page.'],
  ['Retention (keep)', 'Keep only the newest N versions of each package (per name and architecture, per stage). 0 means keep everything. Older versions are purged automatically and logged.'],
  ['Activity', 'The audit log on each repo page: uploads, deletes, promotions and purges, with who did it.'],
  ['Admin API token', 'A secret for scripts and CI, created on the API tokens page. Sent as Authorization: Bearer. Different from a repo user token, which only reads repos.'],
  ['Mirror', 'A first-stage repo that copies an upstream dnf repo instead of taking uploads. It is empty when created and cannot be put behind another repo. Filters and a schedule are optional.'],
  ['Sync', 'One run of a mirror: fetch upstream metadata, download the new rpms that pass the filters, verify them, then publish all at once. Upstream deletions are not mirrored, and a package you delete by hand comes back next sync.'],
  ['Batch', 'The packages added by one sync, numbered. Promote a whole batch to the next stage in one step; the batch id stays with the packages in later stages.'],
  ['Dedup', 'Saving disk by storing identical rpm files (same checksum and size) once and sharing them between repos with hardlinks. Deleting a package from one repo never affects the others.'],
  ['Suite (deb)', 'The apt release name in the sources.list line, such as bookworm or stable. Each deb repo has one; it defaults to the slug.'],
  ['Component (deb)', 'A section inside a suite, such as main or contrib. Chosen when the deb repo is created. A package is promoted only into a stage that has its component.'],
  ['GPG key',`Repo metadata is signed. Clients fetch the public key from ${o}/gpg.key.`],
]

const flows: { id: string; title: string; steps: React.ReactNode[] }[] = [
  { id: 'first-repo', title: 'Create your first repo', steps: [
    <>Open <a href="/">Repos</a> and fill in <em>New repo</em>: a slug (used in the URL), a name, and who can pull.</>,
    <>Leave <em>Promotes from</em> empty for a standalone repo.</>,
    <>Click <em>Create</em>, then open the repo from the list.</>,
  ] },
  { id: 'deb-repo', title: 'Create a deb repo and install from it', steps: [
    <>On <a href="/">Repos</a>, choose <em>Format: deb (apt)</em>, then set the suite, components and architectures (defaults: slug, <code>main</code>, <code>amd64 arm64</code>).</>,
    <>Open the repo and use <em>Upload deb</em>. The file must be named <code>package_version_arch.deb</code> (no epoch in the filename).</>,
    <>On the client run the commands shown on the repo page: save <code>{o}/gpg.key</code> to <code>/etc/apt/keyrings/dnfrepo.asc</code>, add the <code>deb [signed-by=…]</code> line to <code>/etc/apt/sources.list.d/</code>, then <code>sudo apt update</code>.</>,
  ] },
  { id: 'deb-mirror', title: 'Mirror Debian or Ubuntu', steps: [
    <>Create a deb repo and choose <em>Type: Mirror</em>. Its suite, components and architectures are set on the mirror form.</>,
    <>Enter the archive URL (for example <code>https://deb.debian.org/debian/</code>), the suite (for example <code>bookworm</code>), components and architectures. Paste the archive's armored public key and tick <em>Require a valid signature</em> to verify upstream metadata.</>,
    <>Save, then <em>Sync now</em>. Clients use this server's key and the repo's sources.list line, not Debian's.</>,
  ] },
  { id: 'add-packages', title: 'Add packages', steps: [
    <>Open the repo (it must be a first stage). Use <em>Upload rpm</em> to add a file, or add a <em>Source</em> (GitHub <code>owner/name</code> or Plex) and click <em>Run now</em>.</>,
    <>The package appears under <em>Packages</em> and the repo metadata is rebuilt and signed. Delete removes a file and rebuilds again.</>,
  ] },
  { id: 'pipeline', title: 'Set up dev → test → prod', steps: [
    <>Create <code>dev</code> first. It takes uploads and sources.</>,
    <>Create <code>test</code> and choose <em>Promotes from: dev</em>. Then create <code>prod</code> promoting from <code>test</code>.</>,
    <>Link repos while they are still empty: a repo that already has packages or sources cannot be put behind another.</>,
  ] },
  { id: 'promote', title: 'Promote packages', steps: [
    <>Open the repo you want to promote <em>from</em>. Tick the rpm files.</>,
    <>Click <em>Promote N selected → next stage</em>. Files already in the next stage are reported as skipped. A different file with the same name there blocks the whole promotion.</>,
  ] },
  { id: 'client', title: 'Let a machine install from a repo', steps: [
    <>On the client run: <code>sudo dnf config-manager addrepo --from-repofile={o}/r/&lt;slug&gt;/&lt;slug&gt;.repo</code></>,
    <>Then <code>sudo dnf install &lt;package&gt;</code>.</>,
    <>For a <em>users</em> repo: create a repo user on <a href="/users">Repo users</a> (copy the token), grant the user on the repo page under <em>Access</em>, then download the .repo file with the credentials filled in.</>,
    <>For an <em>ips</em> repo: list the client’s IP or range under <em>Access</em>. Behind a reverse proxy, the proxy must be listed in TRUSTED_PROXIES or every client looks like the proxy.</>,
  ] },
  { id: 'mirror', title: 'Mirror a repo', steps: [
    <>Create a repo (empty, first stage), open it and use <em>Mirror</em>. Enter the upstream URL, or paste a <code>.repo</code> file and choose a section. Variables like <code>$releasever</code> are filled in once, at save time.</>,
    <>Optionally set architectures, include/exclude patterns, keep-latest, a GPG key to verify the upstream, and a username and token (needs <code>SECRET_KEY</code> on the server).</>,
    <>Pick a schedule (manual, daily, weekly, monthly, with time zone) and save. Click <em>Sync now</em> to start; a schedule missed while the server was down runs once at startup.</>,
    <>A sync publishes only when it is fully downloaded and verified, so a big first sync shows nothing until it finishes. Watch the status on the Mirror panel. Saving replaces every field except the credentials.</>,
    <>Fedora: make one mirror per release and repo (for example <code>fedora-42</code>, <code>updates-42</code>) and sync on the schedule you test against. Disk use is yours to watch.</>,
  ] },
  { id: 'hub-spoke', title: 'Hub and spoke (dev → QA → prod sites)', steps: [
    <>On the hub, make the repo <em>users</em> mode and create a repo user; copy the token.</>,
    <>On the spoke server, create a repo and open <em>Mirror</em>. Use the hub’s <code>/r/&lt;slug&gt;/</code> URL, the repo user name and the token, and paste the hub’s <code>/gpg.key</code> as the GPG key with <em>require signature</em> on, so the spoke verifies what it pulls.</>,
    <>The spoke signs with its own key. Clients of the spoke must use the spoke’s <code>/gpg.key</code>, not the hub’s.</>,
    <>Promote a batch through the spoke’s own stages as usual.</>,
  ] },
  { id: 'retention', title: 'Keep old versions under control', steps: [
    <>On the repo page open <em>Retention</em>. Set <em>Default versions to keep</em>, and add per-package overrides if some packages need more.</>,
    <>Click <em>Save &amp; apply</em>: it purges immediately and then after every upload or promotion. Each stage has its own limits.</>,
  ] },
  { id: 'dedup', title: 'Save disk space with dedup', steps: [
    <>Promotion already shares files between stages. The same rpm uploaded, mirrored or fetched into different repos is stored once per repo unless dedup is on.</>,
    <>Link new files as they arrive: set <code>DEDUP=true</code> on the server and restart. It is off by default.</>,
    <>Merge duplicates already on disk with an API token: <code>curl -fsS -X POST -H "Authorization: Bearer $TOKEN" {o}/api/dedup</code>. It returns how many files were linked, the bytes saved and how many were skipped. It works whether or not <code>DEDUP</code> is set and publishes nothing new.</>,
    <>Skipped files are missing, changed on disk, or on another filesystem. All repos need to share one filesystem for dedup to work.</>,
  ] },
  { id: 'automate', title: 'Automate from CI', steps: [
    <>Create a token on <a href="/tokens">API tokens</a> and store it as a CI secret.</>,
    <>Upload to a first-stage repo: <code>curl -fsS -H "Authorization: Bearer $TOKEN" -F file=@foo-1.0-1.x86_64.rpm {o}/api/repos/dev/packages</code></>,
    <>Promote: <code>curl -fsS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{'{"filenames":["foo-1.0-1.x86_64.rpm"]}'}' {o}/api/repos/dev/promote</code></>,
    <>Delete: <code>curl -fsS -X DELETE -H "Authorization: Bearer $TOKEN" {o}/api/repos/dev/packages/foo-1.0-1.x86_64.rpm</code></>,
    <>Full reference with responses and error codes: <code>docs/api.md</code> in the project.</>,
  ] },
]

export default function Help() {
  return (
    <>
      <h1>Help</h1>
      <p className="muted">How dnfrepo works and how to do the common jobs.</p>
      <nav className="card toc" aria-label="Help contents">
        <strong>Walkthroughs</strong>
        <ul>{flows.map((f) => <li key={f.id}><a href={`#${f.id}`}>{f.title}</a></li>)}</ul>
        <a href="#terms">Definitions</a>
      </nav>
      {flows.map((f) => (
        <section key={f.id} id={f.id}>
          <h2>{f.title}</h2>
          <ol>{f.steps.map((s, i) => <li key={i}>{s}</li>)}</ol>
        </section>
      ))}
      <section id="terms">
        <h2>Definitions</h2>
        <dl>{terms.map(([t, d]) => <div key={t}><dt><strong>{t}</strong></dt><dd>{d}</dd></div>)}</dl>
      </section>
    </>
  )
}
