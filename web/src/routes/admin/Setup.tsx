import { useEffect, useRef, useState } from "react";
import { keccak256, toBytes, type Address, type Hex } from "viem";
import { AsyncSection } from "../../components/AsyncSection";
import { useAsync } from "../../hooks/useAsync";
import { api, ApiError } from "../../lib/client";
import type { components } from "../../lib/api-types";
import {
  buildProjectConfigV3,
  parseFactoryVersion,
  type FactoryVersion,
  type StrategyChoice,
} from "../../lib/deployConfig";
import { AmountFormatError, toMinimalUnits } from "../../lib/format";
import {
  buildLegal,
  EMPTY_LEGAL_FORM,
  legalFromProfileJson,
  safeHttpsHref,
  type LegalDescriptor,
  type LegalForm,
} from "../../lib/legal";
import { useWalletContext } from "../../context/walletContextValue";
import {
  connectWallet,
  readErc20Decimals,
  sendDeployProject,
  waitForTxReceipt,
  type ProjectConfigInput,
  type ProjectConfigV3Input,
} from "../../lib/wallet";

type Project = components["schemas"]["Project"];
type ValidationResult = components["schemas"]["ValidationResult"];
type StoredProfile = components["schemas"]["StoredProfile"];

// UI deployment state: unknown while loading; not-deployed for 404/Undeployed; otherwise
// Project.status
type DeployStatus =
  "unknown" | "not-deployed" | "Deploying" | "Verifying" | "Active" | "Failed";

// Interval between GET /project polls while a deployment is Deploying or Verifying
const DEPLOY_POLL_MS = 3000;

/**
 * Profile creation/validation and project deployment. Validate checks assetSchema server-side
 * without storage; admin-only creation persists once per projectId and unlocks deployment.
 * The form derives projectId, decimals, tokenUnit and profileDigest from the stored profile.
 */
export function Setup() {
  const project = useAsync<Project>((signal) => api.getProject({ signal }), []);
  // Load the stored profile on mount (404 means absent); it unlocks Deployment after reload
  const storedProfile = useAsync<StoredProfile | null>(
    (signal) => loadStoredProfile(signal),
    [],
  );
  // A profile created in this session overrides the loaded one
  const [createdProfile, setCreatedProfile] = useState<CreatedProfile | null>(
    null,
  );
  const loadedProfile =
    storedProfile.status === "success" && storedProfile.data
      ? storedToCreatedProfile(storedProfile.data)
      : null;
  const profile = createdProfile ?? loadedProfile;

  // Keep deploy status while polls reload to avoid gate flicker and restarting the polling effect
  const [deployStatus, setDeployStatus] = useState<DeployStatus>("unknown");
  const [deployNote, setDeployNote] = useState<string>("");
  useEffect(() => {
    if (project.status === "success") {
      const s = project.data.status;
      setDeployStatus(!s || s === "Undeployed" ? "not-deployed" : s);
      setDeployNote(project.data.verificationNote ?? "");
    } else if (project.status === "error") {
      // GET /project returns 404 before the first deployment
      setDeployStatus("not-deployed");
      setDeployNote("");
    }
    // Retain status during poll reloads
  }, [project.status, project.data]);

  // Poll while Deploying/Verifying; read reload through a ref to restart only on status changes
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
          Configure roles, prices and deployment; validate and create the Asset
          Profile.
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
        {/* Wait for profile load before choosing stored view or creation form */}
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

/*
 * Shows deployment form with a stored profile, progress during Deploying/Verifying,
 * Active confirmation/note with details under Current project, or Failed details plus retry form
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
        <p>Deployment in progress - {deployStatus}…</p>
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
          Deployed - see <strong>Current project</strong> above for the live
          addresses and configuration.
        </p>
        {deployNote && (
          <p className="field__hint">Verification note: {deployNote}</p>
        )}
      </div>
    );
  }

  // Deployment and retry require a stored profile
  if (!profile) {
    return (
      <p className="field__hint">
        Create the Asset Profile above to supply project ID, token decimals and
        profile digest.
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

// Fetches the stored profile; a 404 (none yet) returns null instead of throwing
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

// Maps stored profile to deployment fields, preferring derived identity over raw JSON; retain raw
// JSON for display
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
        <dd>{project.projectId ?? "-"}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Version</dt>
        <dd>{project.version ?? "-"}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Chain ID</dt>
        <dd>{project.chainId ?? "-"}</dd>
      </div>
      <div className="tx-preview__row">
        <dt>Profile digest</dt>
        <dd>{project.profileDigest ?? "-"}</dd>
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

// Stored profile fields passed to Deployment without re-entry
interface CreatedProfile {
  profileDigest: string;
  cid: string;
  projectId: string;
  tokenDecimals: number;
  tokenUnit: string;
  /** The stored profile JSON, pretty-printed and shown read-only */
  rawProfileJson?: string;
}

// Fix version at 1.0 and use configured projectId; operator enters descriptive fields and
// assetSchema
const PROFILE_VERSION = "1.0";

// Empty object-schema starter; server validates the supported dialect
const DEFAULT_ASSET_SCHEMA = JSON.stringify(
  {
    type: "object",
    properties: {},
    required: [],
  },
  null,
  2,
);

// Extract deployment fields; the server supplies the stored digest
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

const SHA_HINT = "Document SHA-256: 64 lowercase hex characters.";

// Legal inputs for a selected wrapper: key, label, hint, input type
const LEGAL_TEXT_FIELDS: [keyof LegalForm, string, string, string?][] = [
  [
    "issuerLei",
    "Issuer LEI",
    "20-character ISO 17442 Legal Entity Identifier.",
  ],
  [
    "holderRights",
    "Holder rights",
    "The holder's rights, in one line of up to 2,048 characters.",
  ],
  [
    "redemptionUri",
    "Redemption terms URI",
    "Published terms. Enter both URI and SHA-256, or neither.",
  ],
  ["redemptionSha256", "Redemption terms SHA-256", SHA_HINT],
  [
    "maturityDate",
    "Maturity date",
    "Informational only; the platform takes no action on this date.",
    "date",
  ],
  [
    "ratingProvider",
    "Rating provider",
    "Required when any rating field is set.",
  ],
  ["ratingGrade", "Rating grade", "Optional, for example A."],
  ["ratingUri", "Rating URI", "Where the rating is published."],
  ["ratingSha256", "Rating SHA-256", SHA_HINT],
];

// Link only https: URLs; show other references as text
function DocRef({ uri, sha256 }: { uri: string; sha256: string }) {
  const href = safeHttpsHref(uri);
  return (
    <>
      {href ? (
        <a href={href} target="_blank" rel="noopener noreferrer">
          {uri}
        </a>
      ) : (
        <span>{uri}</span>
      )}{" "}
      <span className="mono">sha256: {sha256}</span>
    </>
  );
}

function LegalView({ legal }: { legal: LegalDescriptor | null }) {
  if (!legal) return null;
  const row = (label: string, value: React.ReactNode) => (
    <div className="tx-preview__row">
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  );
  return (
    <div className="u-mt-4">
      <h4>Legal descriptor</h4>
      <dl className="tx-preview__grid">
        {row("Wrapper", legal.wrapper)}
        {legal.issuerLei && row("Issuer LEI", legal.issuerLei)}
        {legal.holderRights && row("Holder rights", legal.holderRights)}
        {legal.redemptionTerms &&
          row("Redemption terms", <DocRef {...legal.redemptionTerms} />)}
        {legal.maturityDate && row("Maturity date", legal.maturityDate)}
        {legal.rating &&
          row(
            "Rating",
            <>
              {legal.rating.provider}
              {legal.rating.grade ? ` ${legal.rating.grade}` : ""}{" "}
              <DocRef uri={legal.rating.uri} sha256={legal.rating.sha256} />
            </>,
          )}
      </dl>
    </div>
  );
}

function ProfileEditor({
  createdProfile,
  onProfileCreated,
}: {
  createdProfile: CreatedProfile | null;
  onProfileCreated: (profile: CreatedProfile) => void;
}) {
  // Fixed version and configured projectId match server validation; neither is entered here
  const [projectId, setProjectId] = useState<string | null>(null);
  const [configError, setConfigError] = useState<string | null>(null);
  const [assetType, setAssetType] = useState("");
  const [tokenUnit, setTokenUnit] = useState("");
  const [tokenDecimals, setTokenDecimals] = useState(18);
  const [recordIdLabel, setRecordIdLabel] = useState("");
  const [assetSchemaJson, setAssetSchemaJson] = useState(DEFAULT_ASSET_SCHEMA);
  const [legalForm, setLegalForm] = useState<LegalForm>(EMPTY_LEGAL_FORM);
  const [result, setResult] = useState<ValidationResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [validating, setValidating] = useState(false);
  const [creating, setCreating] = useState(false);

  // Load configured projectId; empty blocks profile creation
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
            "Set contract.project_id in the server config, restart the platform and reload before creating a profile.",
          );
        }
      })
      .catch(() => {
        if (!cancelled)
          setConfigError(
            "Could not load the configured projectId. Check the server and reload.",
          );
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // Assemble form fields and parsed assetSchema; server validates the document
  function buildProfileOrError(): Record<string, unknown> | undefined {
    if (!projectId) {
      setError(
        "Set contract.project_id in the server config before creating a profile.",
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
    const { legal, error: legalError } = buildLegal(legalForm);
    if (legalError) {
      setError(legalError);
      return undefined;
    }
    return {
      profileVersion: PROFILE_VERSION,
      projectId,
      assetType,
      tokenUnit,
      tokenDecimals: Number(tokenDecimals),
      recordIdLabel,
      ...(legal ? { legal } : {}),
      assetSchema,
    };
  }

  function setLegal(key: keyof LegalForm, value: string) {
    setLegalForm((f) => ({ ...f, [key]: value }));
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
        // Profile creation 400 may carry ValidationResult.errors instead of a generic Error; read
        // either
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
      <div role="status">
        <dl className="tx-preview__grid">
          <div className="tx-preview__row">
            <dt>Status</dt>
            <dd>Persisted - immutable for this project</dd>
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
              The stored profile is immutable for this project and read-only
              here.
            </span>
          </div>
        )}
        <LegalView
          legal={legalFromProfileJson(createdProfile.rawProfileJson)}
        />
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
          <dd className="mono">{projectId ?? "- not configured -"}</dd>
        </div>
      </dl>
      <p className="field__hint">
        Project ID comes from server contract.project_id; version is fixed at
        1.0. Configure the ID before creating a profile.
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
          Asset class (e.g. gold, real estate, invoice); 1-128 characters.
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
          Unit per whole token (e.g. gram, sqft, USD); 1-64 characters.
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
          Fractional digits (0-36; e.g. 18). Fixed on-chain at deployment.
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
          Identifier label in record forms (e.g. Serial number, Deed number,
          Invoice ID); 1-128 characters.
        </span>
      </div>

      <fieldset className="field">
        <legend>Legal descriptor (optional)</legend>
        <span className="field__hint" id="legal-hint">
          Describe the represented instrument. Stored with the immutable profile
          without verification; select None to omit.
        </span>
        <div className="field">
          <label htmlFor="legal-wrapper">Wrapper</label>
          <select
            id="legal-wrapper"
            value={legalForm.wrapper}
            onChange={(e) => setLegal("wrapper", e.target.value)}
            aria-describedby="legal-hint"
          >
            <option value="">None</option>
            <option value="share">Share</option>
            <option value="entitlement">Entitlement</option>
            <option value="certificate">Certificate</option>
            <option value="claim">Claim</option>
          </select>
        </div>
        {legalForm.wrapper && (
          <>
            {LEGAL_TEXT_FIELDS.map(([key, label, hint, type]) => (
              <div className="field" key={key}>
                <label htmlFor={`legal-${key}`}>{label}</label>
                <input
                  id={`legal-${key}`}
                  type={type ?? "text"}
                  value={legalForm[key]}
                  onChange={(e) => setLegal(key, e.target.value)}
                  aria-describedby={`legal-${key}-hint`}
                />
                <span className="field__hint" id={`legal-${key}-hint`}>
                  {hint}
                </span>
              </div>
            ))}
          </>
        )}
      </fieldset>

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
          JSON Schema for record metadata and editor placeholders. Enter an
          object; the server enforces the supported dialect.{" "}
          <a
            href="https://json-schema.org/learn/getting-started-step-by-step"
            target="_blank"
            rel="noopener noreferrer"
          >
            JSON Schema documentation
          </a>
          . Validate before saving. Admin-only creation is permanent for this
          project; the stored profile supplies project ID, token unit, decimals
          and digest.
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
        <div role="status">
          <dl className="tx-preview__grid u-mt-4">
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
        </div>
      )}
    </div>
  );
}

// Require addresses that RWAFactory._validateConfig rejects when zero
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

// Bounds from contracts/src/RWAFactory.sol, in seconds
const MIN_REDEMPTION_TIMEOUT = 86_400; // 1 day
const MAX_REDEMPTION_TIMEOUT = 31_536_000; // 365 days

// The v3 fields stay at their neutral values on an rwa-v2 factory and are not sent
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
  strategyKind: "Fixed" as StrategyChoice,
  minPurchase: "0",
  incomeManager: "",
  priceFeed: "",
  priceMaxAge: "",
  purchaseSpreadBps: "0",
  redemptionSpreadBps: "0",
  sequencerUptimeFeed: "",
  sequencerGracePeriod: "",
};

// Loads the factory's version before offering a form, since the ProjectConfig encoding depends on it
function DeployForm({
  profile,
  onDeployed,
}: {
  profile: CreatedProfile;
  onDeployed: () => void;
}) {
  const bootstrap = useAsync<components["schemas"]["BootstrapConfig"]>(
    (signal) => api.getConfig({ signal }),
    [],
  );
  if (bootstrap.status === "error") {
    return (
      <div className="async-state async-state--error" role="alert">
        <p>Could not load factory/chain configuration.</p>
        <button
          type="button"
          className="button button--secondary"
          onClick={bootstrap.reload}
        >
          Retry
        </button>
      </div>
    );
  }
  if (bootstrap.status !== "success") {
    return (
      <div className="async-state async-state--loading" role="status">
        Loading…
      </div>
    );
  }
  const version = parseFactoryVersion(bootstrap.data.factoryVersion);
  if (!version) {
    const reported = bootstrap.data.factoryVersion;
    return (
      <div className="async-state async-state--error" role="alert">
        <p>
          {reported
            ? `Deployment is blocked: the configured factory reports an unsupported version (${reported}). This console deploys rwa-v2 and rwa-v3 factories only. Check contract.factory_address in the server config.`
            : "The server could not read the configured factory's version, so deployment is blocked. Check the chain RPC and contract.factory_address. The server retries after 30 seconds."}
        </p>
        <button
          type="button"
          className="button button--secondary"
          onClick={bootstrap.reload}
        >
          Retry
        </button>
      </div>
    );
  }
  return (
    <DeployFormBody
      profile={profile}
      version={version}
      bootstrap={bootstrap.data}
      onDeployed={onDeployed}
    />
  );
}

function DeployFormBody({
  profile,
  version,
  bootstrap,
  onDeployed,
}: {
  profile: CreatedProfile;
  version: FactoryVersion;
  bootstrap: components["schemas"]["BootstrapConfig"];
  onDeployed: () => void;
}) {
  const v3 = version === "rwa-v3";
  const [form, setForm] = useState(EMPTY_DEPLOY);
  const oracle = v3 && form.strategyKind === "Oracle";
  const [confirming, setConfirming] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [txHash, setTxHash] = useState<string | null>(null);
  // Read quote decimals and broadcast through the admin wallet on the deployment chain
  const wallet = useWalletContext();

  function set<K extends keyof typeof EMPTY_DEPLOY>(
    key: K,
    value: (typeof EMPTY_DEPLOY)[K],
  ) {
    setForm((f) => ({ ...f, [key]: value }));
  }

  // Fixed needs prices and a pricer; Oracle needs a feed and max age instead. rwa-v2 is always fixed
  const strategyReady = oracle
    ? Boolean(form.priceFeed) && Boolean(form.priceMaxAge)
    : Boolean(form.purchasePricePerWholeToken) &&
      Boolean(form.redemptionPricePerWholeToken);
  const readyToReview =
    Boolean(form.name) &&
    Boolean(form.symbol) &&
    strategyReady &&
    form.redemptionTimeout >= MIN_REDEMPTION_TIMEOUT &&
    form.redemptionTimeout <= MAX_REDEMPTION_TIMEOUT &&
    REQUIRED_ADDRESS_FIELDS.every(
      (key) => (oracle && key === "pricer") || Boolean(form[key]),
    ) &&
    (!v3 || Boolean(form.incomeManager));

  async function handleDeploy() {
    setSubmitting(true);
    setError(null);
    setTxHash(null);
    try {
      // Chain and factory come from the public bootstrap config; the wallet broadcasts and the
      // server observes ProjectDeployed
      const factoryAddress = bootstrap.factoryAddress as Address;
      const chainId = bootstrap.chainId;

      // Deploying needs a connected wallet; connect now if login left none
      let from = wallet.address;
      if (!from) {
        try {
          from = (await connectWallet()).address;
        } catch {
          setError("Connect your wallet on the deployment chain.");
          return;
        }
      }

      // Read quote decimals on the checked deployment chain before converting prices; unreadable
      // tokens or excess precision block deployment
      let quoteDecimals: number;
      try {
        quoteDecimals = await readErc20Decimals(
          chainId,
          form.quoteToken as Address,
        );
      } catch (err) {
        console.log(err);
        setError(
          "Cannot read quote-token decimals. Check its address and connect your wallet to the deployment chain, then retry.",
        );
        return;
      }

      // Build config locally: keccak256(profile UUID) for projectId, stored profile decimals/digest
      // for identity
      const projectId = keccak256(toBytes(profile.projectId));
      let projectConfig: ProjectConfigInput | ProjectConfigV3Input;
      if (v3) {
        const built = buildProjectConfigV3(
          {
            name: form.name,
            symbol: form.symbol,
            quoteToken: form.quoteToken,
            strategyKind: form.strategyKind,
            purchasePrice: form.purchasePricePerWholeToken,
            redemptionPrice: form.redemptionPricePerWholeToken,
            minPurchase: form.minPurchase,
            priceFeed: form.priceFeed,
            priceMaxAge: form.priceMaxAge,
            purchaseSpreadBps: form.purchaseSpreadBps,
            redemptionSpreadBps: form.redemptionSpreadBps,
            sequencerUptimeFeed: form.sequencerUptimeFeed,
            sequencerGracePeriod: form.sequencerGracePeriod,
            redemptionTimeout: form.redemptionTimeout,
            admin: form.admin,
            auditor: form.auditor,
            complianceOperator: form.complianceOperator,
            pricer: form.pricer,
            treasurer: form.treasurer,
            redemptionManager: form.redemptionManager,
            incomeManager: form.incomeManager,
            treasury: form.treasury,
            adminTransferDelay: form.adminTransferDelay,
          },
          {
            decimals: profile.tokenDecimals,
            profileDigest: profile.profileDigest as Hex,
            projectId,
            quoteDecimals,
          },
        );
        if ("error" in built) {
          setError(built.error);
          return;
        }
        projectConfig = built.config;
      } else {
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
              : "Invalid price - enter a whole-quote-token amount.",
          );
          return;
        }
        projectConfig = {
          name: form.name,
          symbol: form.symbol,
          decimals: profile.tokenDecimals,
          profileDigest: profile.profileDigest as Hex,
          projectId,
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
      }

      const hash = await sendDeployProject(
        chainId,
        from,
        factoryAddress,
        projectConfig,
      );
      await waitForTxReceipt(hash);
      setTxHash(hash);
      setConfirming(false);
      // Notify parent polling after broadcast; server verification may activate or fail the
      // deployment
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
        Project ID, decimals and digest come from the stored Asset Profile and
        are read-only here.
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
      {v3 && (
        <div className="field">
          <label htmlFor="strategyKind">Price strategy</label>
          <select
            id="strategyKind"
            value={form.strategyKind}
            onChange={(e) =>
              set("strategyKind", e.target.value as StrategyChoice)
            }
            aria-describedby="strategyKind-hint"
          >
            <option value="Fixed">Fixed prices (set by a pricer)</option>
            <option value="Oracle">Oracle feed (Chainlink-style)</option>
          </select>
          <span className="field__hint" id="strategyKind-hint">
            An oracle strategy has no roles and no setters: its feed and spreads
            are fixed at deployment.
          </span>
        </div>
      )}
      {v3 && (
        <div className="field">
          <label htmlFor="minPurchase">
            Minimum purchase (whole quote-token units)
          </label>
          <input
            id="minPurchase"
            value={form.minPurchase}
            onChange={(e) => set("minPurchase", e.target.value)}
            aria-describedby="minPurchase-hint"
          />
          <span className="field__hint" id="minPurchase-hint">
            Minimum quote paid per purchase; 0 disables it. The admin can change
            it later.
          </span>
        </div>
      )}
      {!oracle && (
        <>
          <div className="field">
            <label htmlFor="purchasePrice">
              Purchase price per whole token (whole quote-token units)
            </label>
            <input
              id="purchasePrice"
              value={form.purchasePricePerWholeToken}
              onChange={(e) =>
                set("purchasePricePerWholeToken", e.target.value)
              }
              aria-describedby="purchasePrice-hint"
            />
            <span className="field__hint" id="purchasePrice-hint">
              Whole quote-token units (e.g. 1.5), converted to minimal units in
              the browser before deployment.
            </span>
          </div>
          <div className="field">
            <label htmlFor="redemptionPrice">
              Redemption price per whole token (whole quote-token units)
            </label>
            <input
              id="redemptionPrice"
              value={form.redemptionPricePerWholeToken}
              onChange={(e) =>
                set("redemptionPricePerWholeToken", e.target.value)
              }
              aria-describedby="redemptionPrice-hint"
            />
            <span className="field__hint" id="redemptionPrice-hint">
              Whole quote-token units, converted to minimal units in the browser
              before deployment.
            </span>
          </div>
        </>
      )}
      {oracle && (
        <>
          <div className="field">
            <label htmlFor="priceFeed">Price feed address</label>
            <input
              id="priceFeed"
              className="mono"
              value={form.priceFeed}
              onChange={(e) => set("priceFeed", e.target.value)}
              aria-describedby="priceFeed-hint"
            />
            <span className="field__hint" id="priceFeed-hint">
              Chainlink-style AggregatorV3 feed quoting one whole RWA token in
              the quote token.
            </span>
          </div>
          <div className="field">
            <label htmlFor="priceMaxAge">Max feed age (seconds)</label>
            <input
              id="priceMaxAge"
              inputMode="numeric"
              value={form.priceMaxAge}
              onChange={(e) => set("priceMaxAge", e.target.value)}
              aria-describedby="priceMaxAge-hint"
            />
            <span className="field__hint" id="priceMaxAge-hint">
              New purchases and redemption requests revert when the feed is older
              than this.
            </span>
          </div>
          <div className="field">
            <label htmlFor="purchaseSpreadBps">Purchase spread (bps)</label>
            <input
              id="purchaseSpreadBps"
              inputMode="numeric"
              value={form.purchaseSpreadBps}
              onChange={(e) => set("purchaseSpreadBps", e.target.value)}
              aria-describedby="purchaseSpreadBps-hint"
            />
            <span className="field__hint" id="purchaseSpreadBps-hint">
              Markup over the feed price, 0 to 10,000 (100 bps = 1%).
            </span>
          </div>
          <div className="field">
            <label htmlFor="redemptionSpreadBps">Redemption spread (bps)</label>
            <input
              id="redemptionSpreadBps"
              inputMode="numeric"
              value={form.redemptionSpreadBps}
              onChange={(e) => set("redemptionSpreadBps", e.target.value)}
              aria-describedby="redemptionSpreadBps-hint"
            />
            <span className="field__hint" id="redemptionSpreadBps-hint">
              Discount from the feed price, 0 to 9,999.
            </span>
          </div>
          <div className="field">
            <label htmlFor="sequencerUptimeFeed">
              Sequencer uptime feed (optional)
            </label>
            <input
              id="sequencerUptimeFeed"
              className="mono"
              value={form.sequencerUptimeFeed}
              onChange={(e) => set("sequencerUptimeFeed", e.target.value)}
              aria-describedby="sequencerUptimeFeed-hint"
            />
            <span className="field__hint" id="sequencerUptimeFeed-hint">
              For L2 chains. Set together with the grace period, or leave both
              empty.
            </span>
          </div>
          <div className="field">
            <label htmlFor="sequencerGracePeriod">
              Sequencer grace period (seconds, optional)
            </label>
            <input
              id="sequencerGracePeriod"
              inputMode="numeric"
              value={form.sequencerGracePeriod}
              onChange={(e) => set("sequencerGracePeriod", e.target.value)}
            />
          </div>
        </>
      )}
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
          Between 1 day (86,400s) and 365 days (31,536,000s).
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
      {!oracle && (
        <div className="field">
          <label htmlFor="pricer">Pricer address</label>
          <input
            id="pricer"
            className="mono"
            value={form.pricer}
            onChange={(e) => set("pricer", e.target.value)}
          />
        </div>
      )}
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
      {v3 && (
        <div className="field">
          <label htmlFor="incomeManager">Income manager address</label>
          <input
            id="incomeManager"
            className="mono"
            value={form.incomeManager}
            onChange={(e) => set("incomeManager", e.target.value)}
            aria-describedby="incomeManager-hint"
          />
          <span className="field__hint" id="incomeManager-hint">
            Holds INCOME_MANAGER_ROLE on the IncomeDistributor: creates and
            extends income distributions.
          </span>
        </div>
      )}
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
            Sends RWAFactory.deploy from your wallet on the deployment chain;
            the server verifies ProjectDeployed. Review every field: token
            identity and one-time contract wiring are permanent.
          </p>
          {v3 && (
            <dl className="tx-preview__grid">
              <div className="tx-preview__row">
                <dt>Chain ID</dt>
                <dd>{bootstrap.chainId}</dd>
              </div>
              <div className="tx-preview__row">
                <dt>Factory ({version})</dt>
                <dd className="mono">{bootstrap.factoryAddress}</dd>
              </div>
              <div className="tx-preview__row">
                <dt>Price strategy</dt>
                <dd>{form.strategyKind}</dd>
              </div>
              <div className="tx-preview__row">
                <dt>Minimum purchase</dt>
                <dd>{form.minPurchase || "0"} (whole quote-token units)</dd>
              </div>
              {oracle && (
                <>
                  <div className="tx-preview__row">
                    <dt>Price feed</dt>
                    <dd className="mono">{form.priceFeed}</dd>
                  </div>
                  <div className="tx-preview__row">
                    <dt>Max feed age</dt>
                    <dd>{form.priceMaxAge} s</dd>
                  </div>
                  <div className="tx-preview__row">
                    <dt>Spreads (purchase / redemption)</dt>
                    <dd>
                      {form.purchaseSpreadBps || "0"} /{" "}
                      {form.redemptionSpreadBps || "0"} bps
                    </dd>
                  </div>
                  <div className="tx-preview__row">
                    <dt>Sequencer feed / grace</dt>
                    <dd>
                      {form.sequencerUptimeFeed
                        ? `${form.sequencerUptimeFeed} / ${form.sequencerGracePeriod} s`
                        : "none"}
                    </dd>
                  </div>
                </>
              )}
              <div className="tx-preview__row">
                <dt>Income manager</dt>
                <dd className="mono">{form.incomeManager}</dd>
              </div>
            </dl>
          )}
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
          Deployment broadcast for project <strong>{profile.projectId}</strong>.
          Transaction <span className="mono">{txHash}</span>. Awaiting server
          observation; see <strong>Current project</strong> above.
        </p>
      )}
    </div>
  );
}
