import { useEffect, useRef, useState } from "react";
import { keccak256, toBytes, type Address, type Hex } from "viem";
import { AsyncSection } from "../../components/AsyncSection";
import { useAsync } from "../../hooks/useAsync";
import { api, ApiError } from "../../lib/client";
import type { components } from "../../lib/api-types";
import {
  AmountFormatError,
  toMinimalUnits,
} from "../../lib/format";
import { useWalletContext } from "../../context/walletContextValue";
import {
  connectWallet,
  readErc20Decimals,
  sendDeployProject,
  waitForTxReceipt,
  type ProjectConfigInput,
} from "../../lib/wallet";

type Project = components["schemas"]["Project"];
type ValidationResult = components["schemas"]["ValidationResult"];
type StoredProfile = components["schemas"]["StoredProfile"];

/**
 * The deployment lifecycle as the UI cares about it. "unknown" is the initial
 * pre-first-load value; "not-deployed" folds together a 404 (no project yet)
 * and the server's explicit "Undeployed". The rest mirror Project.status.
 */
type DeployStatus =
  "unknown" | "not-deployed" | "Deploying" | "Verifying" | "Active" | "Failed";

/** How often to re-poll GET /project while a deployment is in a non-terminal state. */
const DEPLOY_POLL_MS = 3000;

/**
 * Setup: Asset Profile editor + validation preview, chain/role config,
 * prices, redemption timeout, deployment, verification.
 *
 * The Asset Profile schema itself is per-project and is not part of the
 * frozen OpenAPI contract (CreateRecordRequest.asset is an untyped object),
 * so the editor below is a JSON editor validated server-side via
 * POST /api/v1/profile/validate rather than a generated form.
 *
 * Setup is an explicit two-step sequence — validate (pure) then create/persist
 * (POST /api/v1/profile, admin-only, create-once) — and Deployment only unlocks
 * once a profile is stored. The deploy form derives projectId, decimals,
 * tokenUnit, and profileDigest from that stored profile instead of letting the
 * browser supply its own, separately-typed copies that could silently disagree
 * with it (the server does not yet derive these itself; see docs/adr for the
 * follow-up once it does).
 */
export function Setup() {
  const project = useAsync<Project>((signal) => api.getProject({ signal }), []);
  // The profile is create-once and persisted server-side, so load it on mount
  // (GET /api/v1/profile, 404 → none yet). Repopulating from it means a reload
  // or navigation lands back on the already-persisted profile with Deployment
  // unlocked, instead of the empty create form that would force re-creation
  // (the server 409s on a second create anyway).
  const storedProfile = useAsync<StoredProfile | null>(
    (signal) => loadStoredProfile(signal),
    [],
  );
  // A profile persisted this session (fresh create) overrides the loaded one;
  // otherwise the loaded profile (if any) is the created/persisted state.
  const [createdProfile, setCreatedProfile] = useState<CreatedProfile | null>(
    null,
  );
  const loadedProfile =
    storedProfile.status === "success" && storedProfile.data
      ? storedToCreatedProfile(storedProfile.data)
      : null;
  const profile = createdProfile ?? loadedProfile;

  // Sticky deploy status derived from GET /project. Kept in state (updated only
  // on a resolved load) rather than read straight off `project` so it survives
  // the brief loading flip each poll's reload causes — otherwise the gate would
  // flicker and the polling effect below would tear itself down mid-cycle.
  const [deployStatus, setDeployStatus] = useState<DeployStatus>("unknown");
  const [deployNote, setDeployNote] = useState<string>("");
  useEffect(() => {
    if (project.status === "success") {
      const s = project.data.status;
      setDeployStatus(!s || s === "Undeployed" ? "not-deployed" : s);
      setDeployNote(project.data.verificationNote ?? "");
    } else if (project.status === "error") {
      // GET /project 404s before the first deploy — treat as not-yet-deployed.
      setDeployStatus("not-deployed");
      setDeployNote("");
    }
    // On "loading" (a poll's in-flight reload) keep the last known status.
  }, [project.status, project.data]);

  // Poll GET /project while the deployment is mid-flight so the admin always
  // sees current status, stopping at a terminal state (Active/Failed) and on
  // unmount. reload's identity changes each render, so read it through a ref to
  // keep the interval keyed only on the (sticky) status.
  const reloadRef = useRef(project.reload);
  reloadRef.current = project.reload;
  useEffect(() => {
    if (deployStatus !== "Deploying" && deployStatus !== "Verifying") return;
    const id = setInterval(() => reloadRef.current(), DEPLOY_POLL_MS);
    return () => clearInterval(id);
  }, [deployStatus]);

  return (
    <div>
      <header className="app-main__header">
        <h1>Setup</h1>
        <p>
          Chain and role configuration, pricing, deployment, and profile
          validation.
        </p>
      </header>

      <section className="card">
        <h2>Current project</h2>
        <AsyncSection state={project} onRetry={project.reload}>
          {(data) => <ProjectSummary project={data} />}
        </AsyncSection>
      </section>

      <section className="card">
        <h2>Asset Profile</h2>
        {/* Wait for the load before rendering the editor so the empty create
            form never flashes in front of an already-persisted profile. */}
        <AsyncSection state={storedProfile} onRetry={storedProfile.reload}>
          {() => (
            <ProfileEditor
              createdProfile={profile}
              onProfileCreated={setCreatedProfile}
            />
          )}
        </AsyncSection>
      </section>

      <section className="card">
        <h2>Deployment</h2>
        <DeploymentSection
          deployStatus={deployStatus}
          deployNote={deployNote}
          profileLoading={storedProfile.status === "loading"}
          profile={profile}
          onDeployed={project.reload}
        />
      </section>
    </div>
  );
}

/**
 * Gates the Deployment area on the deploy lifecycle:
 * - not-yet-deployed + a persisted profile → the deploy form;
 * - Deploying/Verifying → an in-progress indicator (form hidden), while the
 *   parent polls GET /project;
 * - Active → hidden (the deployed details live in "Current project"), with a
 *   small confirmation and any non-fatal verification note;
 * - Failed → the failure + note, plus the deploy form so the admin can retry
 *   (a 409 from re-deploying an existing record surfaces as the form's error).
 */
function DeploymentSection({
  deployStatus,
  deployNote,
  profileLoading,
  profile,
  onDeployed,
}: {
  deployStatus: DeployStatus;
  deployNote: string;
  profileLoading: boolean;
  profile: CreatedProfile | null;
  onDeployed: () => void;
}) {
  if (deployStatus === "unknown" || profileLoading) {
    return (
      <div className="async-state async-state--loading" role="status">
        Loading…
      </div>
    );
  }

  if (deployStatus === "Deploying" || deployStatus === "Verifying") {
    return (
      <div className="async-state async-state--loading" role="status">
        <p>Deployment in progress — {deployStatus}…</p>
        <p className="field__hint">
          This updates automatically until the deployment completes.
        </p>
      </div>
    );
  }

  if (deployStatus === "Active") {
    return (
      <div role="status">
        <p>
          Deployed — see <strong>Current project</strong> above for the live
          addresses and configuration.
        </p>
        {deployNote && (
          <p className="field__hint">Verification note: {deployNote}</p>
        )}
      </div>
    );
  }

  // not-deployed or Failed: (re)deploying needs a persisted profile.
  if (!profile) {
    return (
      <p className="field__hint">
        Create and persist an Asset Profile above before deploying — the
        deployment&apos;s project ID, token decimals, and profile digest are
        derived from it.
      </p>
    );
  }

  return (
    <div>
      {deployStatus === "Failed" && (
        <div className="async-state async-state--error" role="alert">
          <p>
            Deployment failed{deployNote ? `: ${deployNote}` : "."} Review the
            configuration below and retry.
          </p>
        </div>
      )}
      <DeployForm profile={profile} onDeployed={onDeployed} />
    </div>
  );
}

/** Fetches the stored profile, mapping the admin-only 404 (none yet) to null rather than an error. */
async function loadStoredProfile(
  signal: AbortSignal,
): Promise<StoredProfile | null> {
  try {
    return await api.getProfile({ signal });
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return null;
    throw err;
  }
}

/**
 * Maps a persisted StoredProfile onto the CreatedProfile the DeployForm needs.
 * Prefers the server-derived identity fields; where those are absent, falls
 * back to reading them off the raw profile document (same fields the editor
 * extracts on create). Carries the raw JSON so the admin can see what's stored.
 */
function storedToCreatedProfile(stored: StoredProfile): CreatedProfile {
  const fromRaw = extractProfileFields(stored.profile);
  return {
    profileDigest: stored.profileDigest,
    cid: stored.cid ?? "",
    projectId: stored.projectId || fromRaw.projectId,
    tokenDecimals: stored.decimals ?? fromRaw.tokenDecimals,
    tokenUnit: stored.tokenUnit ?? fromRaw.tokenUnit,
    rawProfileJson: JSON.stringify(stored.profile, null, 2),
  };
}

function ProjectSummary({ project }: { project: Project }) {
  return (
    <dl className="tx-preview__grid">
      <div className="tx-preview__row">
        <dt>Project ID</dt>
        <dd>{project.projectId ?? "—"}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Version</dt>
        <dd>{project.version ?? "—"}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Chain ID</dt>
        <dd>{project.chainId ?? "—"}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Profile digest</dt>
        <dd>{project.profileDigest ?? "—"}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Paused</dt>
        <dd>{project.paused ? "Yes" : "No"}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Auditor</dt>
        <dd title={project.auditor}>{project.auditor}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Treasury</dt>
        <dd title={project.treasury}>{project.treasury}</dd>
      </div>
      {project.addresses &&
        Object.entries(project.addresses).map(([key, value]) => (
          <div className="tx-preview__row" key={key}>
            <dt>{key}</dt>
            <dd title={value}>{value}</dd>
          </div>
        ))}
    </dl>
  );
}

/** What Deployment needs from a persisted profile — sourced from it, never re-typed by hand. */
interface CreatedProfile {
  profileDigest: string;
  cid: string;
  projectId: string;
  tokenDecimals: number;
  tokenUnit: string;
  /** The raw profile JSON (pretty-printed), shown read-only so the admin can see what's persisted. */
  rawProfileJson?: string;
}

/** The Asset Profile format is pinned at 1.0 and the projectId is generated, so
 * neither is asked of the operator — they are supplied automatically on create
 * (see ProfileEditor). Only the descriptive fields and the per-asset schema are
 * entered by hand. */
const PROFILE_VERSION = "1.0";

/** Starter template for the per-asset JSON Schema — a minimal object schema the
 * operator fills in. Kept small on purpose; the schema is validated server-side
 * (closed dialect) when Validate is clicked. */
const DEFAULT_ASSET_SCHEMA = JSON.stringify(
  {
    type: "object",
    properties: {},
    required: [],
  },
  null,
  2,
);

/** Best-effort client-side read of the fields Deployment needs — the server is the actual source of truth for the stored digest. */
function extractProfileFields(parsed: unknown): {
  projectId: string;
  tokenDecimals: number;
  tokenUnit: string;
} {
  const obj = (parsed && typeof parsed === "object" ? parsed : {}) as Record<
    string,
    unknown
  >;
  return {
    projectId: typeof obj.projectId === "string" ? obj.projectId : "",
    tokenDecimals:
      typeof obj.tokenDecimals === "number" ? obj.tokenDecimals : 0,
    tokenUnit: typeof obj.tokenUnit === "string" ? obj.tokenUnit : "",
  };
}

function ProfileEditor({
  createdProfile,
  onProfileCreated,
}: {
  createdProfile: CreatedProfile | null;
  onProfileCreated: (profile: CreatedProfile) => void;
}) {
  // profileVersion is pinned and projectId comes from the server config (the
  // operator sets contract.project_id before running the platform) — neither is
  // asked of the operator here. We read the projectId from GET /api/v1/config so
  // it matches the value the server gates every profile against. The rest are
  // entered as discrete, labelled fields rather than as one hand-authored JSON
  // blob.
  const [projectId, setProjectId] = useState<string | null>(null);
  const [configError, setConfigError] = useState<string | null>(null);
  const [assetType, setAssetType] = useState("");
  const [tokenUnit, setTokenUnit] = useState("");
  const [tokenDecimals, setTokenDecimals] = useState(18);
  const [recordIdLabel, setRecordIdLabel] = useState("");
  const [assetSchemaJson, setAssetSchemaJson] = useState(DEFAULT_ASSET_SCHEMA);
  const [result, setResult] = useState<ValidationResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [validating, setValidating] = useState(false);
  const [creating, setCreating] = useState(false);

  // Load the deployment's projectId from the server. An empty value means the
  // operator has not set contract.project_id yet — profile creation is blocked
  // until they do, since the server would reject a mismatched (or missing) one.
  useEffect(() => {
    let cancelled = false;
    api
      .getConfig()
      .then((cfg) => {
        if (cancelled) return;
        if (cfg.projectId) {
          setProjectId(cfg.projectId);
        } else {
          setConfigError(
            "The server has no project_id configured. Set contract.project_id in the server config, restart the platform, then reload this page before creating a profile.",
          );
        }
      })
      .catch(() => {
        if (!cancelled)
          setConfigError(
            "Couldn't load the server configuration to read the projectId. Check the server is running and reload.",
          );
      });
    return () => {
      cancelled = true;
    };
  }, []);

  /** Assembles the full profile document from the discrete fields, parsing the
   * only free-form part (assetSchema). The server is the source of truth for
   * validity — this just gets a well-formed object onto the wire. */
  function buildProfileOrError(): Record<string, unknown> | undefined {
    if (!projectId) {
      setError(
        "No projectId is configured on the server yet — set contract.project_id in the server config before creating a profile.",
      );
      return undefined;
    }
    let assetSchema: unknown;
    try {
      assetSchema = JSON.parse(assetSchemaJson);
    } catch {
      setError("Asset schema must be valid JSON.");
      return undefined;
    }
    return {
      profileVersion: PROFILE_VERSION,
      projectId,
      assetType,
      tokenUnit,
      tokenDecimals: Number(tokenDecimals),
      recordIdLabel,
      assetSchema,
    };
  }

  async function handleValidate() {
    setError(null);
    setResult(null);
    const built = buildProfileOrError();
    if (built === undefined) return;
    setValidating(true);
    try {
      setResult(await api.validateProfile(built as Record<string, never>));
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : "Validation request failed.",
      );
    } finally {
      setValidating(false);
    }
  }

  async function handleCreate() {
    setError(null);
    const built = buildProfileOrError();
    if (built === undefined) return;
    setCreating(true);
    try {
      const created = await api.createProfile(
        built as Record<string, never>,
        `create-profile-${projectId}`,
      );
      onProfileCreated({
        profileDigest: created.profileDigest ?? "",
        cid: created.cid ?? "",
        ...extractProfileFields(built),
        rawProfileJson: JSON.stringify(built, null, 2),
      });
    } catch (err) {
      if (err instanceof ApiError) {
        // POST /api/v1/profile's 400 body is a ValidationResult (errors[]),
        // not the generic Error{code,message} every other endpoint uses —
        // read whichever shape actually came back.
        const body = err.body as unknown as
          Partial<ValidationResult> | undefined;
        setError(
          body?.errors && body.errors.length > 0
            ? `Profile invalid: ${body.errors.join("; ")}`
            : (err.message ?? "Profile creation failed."),
        );
      } else {
        setError("Profile creation failed.");
      }
    } finally {
      setCreating(false);
    }
  }

  if (createdProfile) {
    return (
      <div>
        <dl className="tx-preview__grid" role="status">
          <div className="tx-preview__row">
            <dt>Status</dt>
            <dd>Persisted — immutable for the life of this deployment</dd>
          </div>
          <div className="tx-preview__row">
            <dt>Project ID</dt>
            <dd>{createdProfile.projectId}</dd>
          </div>
          <div className="tx-preview__row">
            <dt>Token unit</dt>
            <dd>{createdProfile.tokenUnit}</dd>
          </div>
          <div className="tx-preview__row">
            <dt>Token decimals</dt>
            <dd>{createdProfile.tokenDecimals}</dd>
          </div>
          <div className="tx-preview__row">
            <dt>Profile digest</dt>
            <dd className="mono">{createdProfile.profileDigest}</dd>
          </div>
          <div className="tx-preview__row">
            <dt>IPFS CID</dt>
            <dd className="mono">{createdProfile.cid}</dd>
          </div>
        </dl>
        {createdProfile.rawProfileJson && (
          <div className="field u-mt-4">
            <label htmlFor="stored-profile-json">
              Stored Asset Profile JSON
            </label>
            <textarea
              id="stored-profile-json"
              rows={12}
              className="mono"
              readOnly
              value={createdProfile.rawProfileJson}
              aria-describedby="stored-profile-json-hint"
            />
            <span className="field__hint" id="stored-profile-json-hint">
              This profile is persisted and immutable for the life of the
              deployment — shown read-only.
            </span>
          </div>
        )}
      </div>
    );
  }

  return (
    <div>
      {configError && (
        <p className="async-state--error" role="alert">
          {configError}
        </p>
      )}
      <dl className="tx-preview__grid">
        <div className="tx-preview__row">
          <dt>Project ID</dt>
          <dd className="mono">{projectId ?? "— not configured —"}</dd>
        </div>
      </dl>
      <p className="field__hint">
        The project ID (a UUID) comes from the server configuration
        (contract.project_id) and the profile version (1.0) is fixed — you
        don&apos;t enter either. If the project ID shows &quot;not
        configured&quot;, set it in the server config before creating a profile.
      </p>

      <div className="field">
        <label htmlFor="assetType">Asset type</label>
        <input
          id="assetType"
          value={assetType}
          onChange={(e) => setAssetType(e.target.value)}
          aria-describedby="assetType-hint"
        />
        <span className="field__hint" id="assetType-hint">
          The class of real-world asset this token represents (e.g. gold,
          real-estate, invoice). 1–128 characters.
        </span>
      </div>

      <div className="field">
        <label htmlFor="tokenUnit">Token unit</label>
        <input
          id="tokenUnit"
          value={tokenUnit}
          onChange={(e) => setTokenUnit(e.target.value)}
          aria-describedby="tokenUnit-hint"
        />
        <span className="field__hint" id="tokenUnit-hint">
          The unit one whole token stands for (e.g. gram, sqft, USD). 1–64
          characters.
        </span>
      </div>

      <div className="field">
        <label htmlFor="tokenDecimals">Token decimals</label>
        <input
          id="tokenDecimals"
          type="number"
          min={0}
          max={36}
          value={tokenDecimals}
          onChange={(e) => setTokenDecimals(Number(e.target.value))}
          aria-describedby="tokenDecimals-hint"
        />
        <span className="field__hint" id="tokenDecimals-hint">
          How many fractional digits the token supports (0–36; e.g. 18, like
          most ERC-20s). This is fixed on-chain at deploy.
        </span>
      </div>

      <div className="field">
        <label htmlFor="recordIdLabel">Record ID label</label>
        <input
          id="recordIdLabel"
          value={recordIdLabel}
          onChange={(e) => setRecordIdLabel(e.target.value)}
          aria-describedby="recordIdLabel-hint"
        />
        <span className="field__hint" id="recordIdLabel-hint">
          What each asset record&apos;s identifier means (e.g. Serial number,
          Deed number, Invoice ID). Shown in record forms. 1–128 characters.
        </span>
      </div>

      <div className="field">
        <label htmlFor="assetSchema">Asset schema</label>
        <textarea
          id="assetSchema"
          rows={10}
          className="mono"
          value={assetSchemaJson}
          onChange={(e) => setAssetSchemaJson(e.target.value)}
          aria-describedby="assetSchema-hint"
        />
        <span className="field__hint" id="assetSchema-hint">
          A JSON Schema describing the metadata each asset record must provide —
          it&apos;s what the metadata form is generated from and validated
          against. Enter a JSON Schema object (a restricted dialect is enforced
          server-side on Validate).{" "}
          <a
            href="https://json-schema.org/learn/getting-started-step-by-step"
            target="_blank"
            rel="noopener noreferrer"
          >
            JSON Schema documentation
          </a>
          . Validate first (pure — no persistence). Creating is admin-only and
          permanent: once stored, a profile cannot be edited or replaced, and
          the deployment below derives its project ID, token unit, decimals, and
          profile digest from it.
        </span>
      </div>

      <button
        type="button"
        className="button button--secondary"
        onClick={handleValidate}
        disabled={!projectId || validating || creating}
      >
        {validating ? "Validating…" : "Validate profile"}
      </button>
      <button
        type="button"
        className="button button--primary"
        onClick={handleCreate}
        disabled={!projectId || !result?.valid || validating || creating}
      >
        {creating ? "Creating…" : "Create & persist profile"}
      </button>

      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}

      {result && (
        <dl className="tx-preview__grid u-mt-4" role="status">
          <div className="tx-preview__row">
            <dt>Valid</dt>
            <dd>{result.valid ? "Yes" : "No"}</dd>
          </div>
          {result.profileDigest && (
            <div className="tx-preview__row">
              <dt>Profile digest</dt>
              <dd>{result.profileDigest}</dd>
            </div>
          )}
          {result.cid && (
            <div className="tx-preview__row">
              <dt>IPFS CID</dt>
              <dd>{result.cid}</dd>
            </div>
          )}
          {result.errors && result.errors.length > 0 && (
            <div className="tx-preview__row">
              <dt>Errors</dt>
              <dd>
                <ul>
                  {result.errors.map((e, i) => (
                    <li key={i}>{e}</li>
                  ))}
                </ul>
              </dd>
            </div>
          )}
        </dl>
      )}
    </div>
  );
}

// Every one of these is a hard revert in RWAFactory._validateConfig (a zero
// address for any of them reverts InvalidConfig), so setup must render and
// require all of them, not just Admin/Auditor.
const REQUIRED_ADDRESS_FIELDS = [
  "quoteToken",
  "admin",
  "auditor",
  "complianceOperator",
  "pricer",
  "treasurer",
  "redemptionManager",
  "treasury",
] as const;

const MIN_REDEMPTION_TIMEOUT = 86_400; // 1 day, contracts/src/RWAFactory.sol
const MAX_REDEMPTION_TIMEOUT = 31_536_000; // 365 days

const EMPTY_DEPLOY = {
  name: "",
  symbol: "",
  quoteToken: "",
  purchasePricePerWholeToken: "",
  redemptionPricePerWholeToken: "",
  redemptionTimeout: 604800,
  admin: "",
  auditor: "",
  complianceOperator: "",
  pricer: "",
  treasurer: "",
  redemptionManager: "",
  treasury: "",
  adminTransferDelay: 172800,
};

function DeployForm({
  profile,
  onDeployed,
}: {
  profile: CreatedProfile;
  onDeployed: () => void;
}) {
  const [form, setForm] = useState(EMPTY_DEPLOY);
  const [confirming, setConfirming] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [txHash, setTxHash] = useState<string | null>(null);
  // The admin is always wallet-connected after signing in (WalletProvider); the
  // deploy is broadcast from that wallet, and the quote token's decimals are
  // read against the deployment chain the wallet must be on.
  const wallet = useWalletContext();

  function set<K extends keyof typeof EMPTY_DEPLOY>(
    key: K,
    value: (typeof EMPTY_DEPLOY)[K],
  ) {
    setForm((f) => ({ ...f, [key]: value }));
  }

  const readyToReview =
    Boolean(form.name) &&
    Boolean(form.symbol) &&
    Boolean(form.purchasePricePerWholeToken) &&
    Boolean(form.redemptionPricePerWholeToken) &&
    form.redemptionTimeout >= MIN_REDEMPTION_TIMEOUT &&
    form.redemptionTimeout <= MAX_REDEMPTION_TIMEOUT &&
    REQUIRED_ADDRESS_FIELDS.every((key) => Boolean(form[key]));

  async function handleDeploy() {
    setSubmitting(true);
    setError(null);
    setTxHash(null);
    try {
      // Deployment is now broadcast from the admin's own wallet
      // (RWAFactory.deploy is permissionless); the server observes the
      // ProjectDeployed event. The factory address + deployment chain come from
      // GET /api/v1/config, not GET /project — the project doesn't exist yet, so
      // /project still 404s.
      let bootstrap: components["schemas"]["BootstrapConfig"];
      try {
        bootstrap = await api.getConfig();
      } catch {
        setError(
          "Couldn't load the bootstrap config (factory address and chain). Try again.",
        );
        return;
      }
      const factoryAddress = bootstrap.factoryAddress as Address;
      const chainId = bootstrap.chainId;

      // The deploy needs a connected wallet on the deployment chain. The admin
      // is normally already connected from login; connect on demand if not.
      let from = wallet.address;
      if (!from) {
        try {
          from = (await connectWallet()).address;
        } catch {
          setError("Connect your wallet on the deployment chain to deploy.");
          return;
        }
      }

      // The two price inputs are entered in WHOLE quote-token units; convert
      // them to the quote token's minimal integer units for ProjectConfig. The
      // project doesn't exist yet, so there's no Project.quoteDecimals to lean
      // on — read the quote token's decimals() directly on-chain against the
      // deployment chain (this also asserts the wallet is on that chain). A
      // bad/unreadable quote token or over-precise prices must block the deploy
      // rather than submit a mis-scaled one.
      let quoteDecimals: number;
      try {
        quoteDecimals = await readErc20Decimals(
          chainId,
          form.quoteToken as Address,
        );
      } catch (err) {
        console.log(err);
        setError(
          "Couldn't read the quote token's decimals on-chain. Check the quote token address and that your wallet is connected to the deployment chain, then try again.",
        );
        return;
      }

      let purchasePricePerWholeToken: string;
      let redemptionPricePerWholeToken: string;
      try {
        purchasePricePerWholeToken = toMinimalUnits(
          form.purchasePricePerWholeToken,
          quoteDecimals,
        );
        redemptionPricePerWholeToken = toMinimalUnits(
          form.redemptionPricePerWholeToken,
          quoteDecimals,
        );
      } catch (err) {
        setError(
          err instanceof AmountFormatError
            ? err.message
            : "Invalid price — enter a whole-quote-token amount.",
        );
        return;
      }

      // Assemble ProjectConfig client-side. projectId is the keccak256 of the
      // profile's UUID string — CRITICAL PARITY with the server, which computes
      // keccak256(UUID-string-bytes) for the on-chain bytes32 projectId. decimals
      // and profileDigest come from the persisted, digest-verified Asset Profile.
      const projectConfig: ProjectConfigInput = {
        name: form.name,
        symbol: form.symbol,
        decimals: profile.tokenDecimals,
        profileDigest: profile.profileDigest as Hex,
        projectId: keccak256(toBytes(profile.projectId)),
        quoteToken: form.quoteToken as Address,
        purchasePricePerWholeToken: BigInt(purchasePricePerWholeToken),
        redemptionPricePerWholeToken: BigInt(redemptionPricePerWholeToken),
        redemptionTimeout: BigInt(form.redemptionTimeout),
        admin: form.admin as Address,
        auditor: form.auditor as Address,
        complianceOperator: form.complianceOperator as Address,
        pricer: form.pricer as Address,
        treasurer: form.treasurer as Address,
        redemptionManager: form.redemptionManager as Address,
        treasury: form.treasury as Address,
        adminTransferDelay: form.adminTransferDelay,
      };

      const hash = await sendDeployProject(
        chainId,
        from,
        factoryAddress,
        projectConfig,
      );
      await waitForTxReceipt(hash);
      setTxHash(hash);
      setConfirming(false);
      // The server picks up the ProjectDeployed event and advances GET /project
      // through Verifying → Active; the parent's sticky status polling shows it.
      onDeployed();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Deployment failed.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div>
      <dl className="tx-preview__grid">
        <div className="tx-preview__row">
          <dt>Project ID</dt>
          <dd>{profile.projectId}</dd>
        </div>
        <div className="tx-preview__row">
          <dt>Decimals</dt>
          <dd>{profile.tokenDecimals}</dd>
        </div>
        <div className="tx-preview__row">
          <dt>Profile digest</dt>
          <dd className="mono">{profile.profileDigest}</dd>
        </div>
      </dl>
      <p className="field__hint">
        Project ID, decimals, and profile digest above come from the persisted
        Asset Profile and are not editable here.
      </p>

      <div className="field">
        <label htmlFor="name">Name</label>
        <input
          id="name"
          value={form.name}
          onChange={(e) => set("name", e.target.value)}
        />
      </div>
      <div className="field">
        <label htmlFor="symbol">Symbol</label>
        <input
          id="symbol"
          value={form.symbol}
          onChange={(e) => set("symbol", e.target.value)}
        />
      </div>
      <div className="field">
        <label htmlFor="quoteToken">Quote token address</label>
        <input
          id="quoteToken"
          className="mono"
          value={form.quoteToken}
          onChange={(e) => set("quoteToken", e.target.value)}
        />
      </div>
      <div className="field">
        <label htmlFor="purchasePrice">
          Purchase price per whole token (whole quote-token units)
        </label>
        <input
          id="purchasePrice"
          value={form.purchasePricePerWholeToken}
          onChange={(e) => set("purchasePricePerWholeToken", e.target.value)}
          aria-describedby="purchasePrice-hint"
        />
        <span className="field__hint" id="purchasePrice-hint">
          Enter whole quote-token units (e.g. 1.5). Converted to the quote
          token&apos;s minimal units on-chain at deploy.
        </span>
      </div>
      <div className="field">
        <label htmlFor="redemptionPrice">
          Redemption price per whole token (whole quote-token units)
        </label>
        <input
          id="redemptionPrice"
          value={form.redemptionPricePerWholeToken}
          onChange={(e) => set("redemptionPricePerWholeToken", e.target.value)}
          aria-describedby="redemptionPrice-hint"
        />
        <span className="field__hint" id="redemptionPrice-hint">
          Enter whole quote-token units. Converted to the quote token&apos;s
          minimal units on-chain at deploy.
        </span>
      </div>
      <div className="field">
        <label htmlFor="redemptionTimeout">Redemption timeout (seconds)</label>
        <input
          id="redemptionTimeout"
          type="number"
          min={MIN_REDEMPTION_TIMEOUT}
          max={MAX_REDEMPTION_TIMEOUT}
          value={form.redemptionTimeout}
          onChange={(e) => set("redemptionTimeout", Number(e.target.value))}
          aria-describedby="redemptionTimeout-hint"
        />
        <span className="field__hint" id="redemptionTimeout-hint">
          Must be between 1 day (86,400s) and 365 days (31,536,000s).
        </span>
      </div>
      <div className="field">
        <label htmlFor="admin">Admin address</label>
        <input
          id="admin"
          className="mono"
          value={form.admin}
          onChange={(e) => set("admin", e.target.value)}
        />
      </div>
      <div className="field">
        <label htmlFor="auditor">Auditor address</label>
        <input
          id="auditor"
          className="mono"
          value={form.auditor}
          onChange={(e) => set("auditor", e.target.value)}
        />
      </div>
      <div className="field">
        <label htmlFor="complianceOperator">Compliance operator address</label>
        <input
          id="complianceOperator"
          className="mono"
          value={form.complianceOperator}
          onChange={(e) => set("complianceOperator", e.target.value)}
        />
      </div>
      <div className="field">
        <label htmlFor="pricer">Pricer address</label>
        <input
          id="pricer"
          className="mono"
          value={form.pricer}
          onChange={(e) => set("pricer", e.target.value)}
        />
      </div>
      <div className="field">
        <label htmlFor="treasurer">Treasurer address</label>
        <input
          id="treasurer"
          className="mono"
          value={form.treasurer}
          onChange={(e) => set("treasurer", e.target.value)}
        />
      </div>
      <div className="field">
        <label htmlFor="redemptionManager">Redemption manager address</label>
        <input
          id="redemptionManager"
          className="mono"
          value={form.redemptionManager}
          onChange={(e) => set("redemptionManager", e.target.value)}
        />
      </div>
      <div className="field">
        <label htmlFor="treasury">Treasury address</label>
        <input
          id="treasury"
          className="mono"
          value={form.treasury}
          onChange={(e) => set("treasury", e.target.value)}
        />
      </div>
      <div className="field">
        <label htmlFor="adminTransferDelay">
          Admin transfer delay (seconds)
        </label>
        <input
          id="adminTransferDelay"
          type="number"
          min={0}
          value={form.adminTransferDelay}
          onChange={(e) => set("adminTransferDelay", Number(e.target.value))}
        />
      </div>

      {!confirming && (
        <button
          type="button"
          className="button button--primary"
          onClick={() => setConfirming(true)}
          disabled={!readyToReview}
        >
          Review deployment
        </button>
      )}

      {confirming && (
        <div className="tx-preview">
          <h3 className="tx-preview__title">Confirm deployment</h3>
          <p>
            This broadcasts RWAFactory.deploy from your connected wallet on the
            deployment chain; the server then observes the on-chain
            ProjectDeployed event. Review every field before confirming —
            deployment parameters cannot be changed after the contracts are live.
          </p>
          <div className="tx-preview__actions">
            <button
              type="button"
              className="button button--primary"
              onClick={handleDeploy}
              disabled={submitting}
            >
              {submitting ? "Deploying…" : "Confirm deploy"}
            </button>
            <button
              type="button"
              className="button button--secondary"
              onClick={() => setConfirming(false)}
              disabled={submitting}
            >
              Back
            </button>
          </div>
        </div>
      )}

      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}

      {txHash && (
        <p role="status">
          Deployment broadcast for project{" "}
          <strong>{profile.projectId}</strong>. Transaction{" "}
          <span className="mono">{txHash}</span>. Waiting for the server to
          observe it — see <strong>Current project</strong> above.
        </p>
      )}
    </div>
  );
}
