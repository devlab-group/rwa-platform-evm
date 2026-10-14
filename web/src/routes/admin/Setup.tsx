import { useEffect, useRef, useState } from "react";
import { keccak256, toBytes, type Address, type Hex } from "viem";
import { AsyncSection } from "../../components/AsyncSection";
import { useAsync } from "../../hooks/useAsync";
import { api, ApiError } from "../../lib/client";
import type { components } from "../../lib/api-types";
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
} from "../../lib/wallet";

type Project = components["schemas"]["Project"];
type ValidationResult = components["schemas"]["ValidationResult"];
type StoredProfile = components["schemas"]["StoredProfile"];

// The deployment lifecycle shown in the UI. "unknown" holds until the first
// load finishes. "not-deployed" covers both a 404 (no project yet) and the
// server's "Undeployed" status. The other values are Project.status.
type DeployStatus =
  "unknown" | "not-deployed" | "Deploying" | "Verifying" | "Active" | "Failed";

// Interval between GET /project polls while a deployment is Deploying or Verifying
const DEPLOY_POLL_MS = 3000;

/**
 * Asset Profile creation and validation, role and chain setup, prices, timeout,
 * deployment and verification. The JSON assetSchema is validated server-side
 * through POST /api/v1/profile/validate because CreateRecordRequest.asset is untyped.
 *
 * Validation stores nothing; admin-only POST /api/v1/profile creates the profile
 * once and unlocks deployment. The deploy form derives projectId, decimals,
 * tokenUnit and profileDigest from that stored profile.
 */
export function Setup() {
  const project = useAsync<Project>((signal) => api.getProject({ signal }), []);
  // The profile is create-once and stored server-side, so load it on mount
  // (GET /api/v1/profile; 404 means none yet). After a reload the page then
  // shows the stored profile with Deployment unlocked; the server would answer
  // a second create with 409.
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

  // Deploy status derived from GET /project. It changes only when a load
  // resolves, so it stays steady while each poll's reload briefly reports
  // "loading"; otherwise the deployment gate would flicker and the polling
  // effect below would restart mid-cycle.
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
    // While a poll reloads ("loading"), keep the last status
  }, [project.status, project.data]);

  // Poll GET /project while the deployment is Deploying or Verifying, until it
  // reaches Active or Failed or the page unmounts. reload changes identity on
  // every render, so the interval reads it through a ref and restarts only when
  // the status changes.
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
        {/* Render the editor only after the load, so the empty create form does
            not flash before a stored profile appears */}
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
 * Shows the Deployment area for the deploy lifecycle:
 * - not deployed with a stored profile: the deploy form;
 * - Deploying or Verifying: a progress indicator in place of the form while the
 *   parent polls GET /project;
 * - Active: a short confirmation and any verification note; the deployed
 *   details are under "Current project";
 * - Failed: the failure and its note, plus the deploy form for a retry.
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

  // Deploying, or retrying after Failed, needs a stored profile
  if (!profile) {
    return (
      <p className="field__hint">
        Create the Asset Profile above before deploying. It supplies the project
        ID, token decimals and profile digest.
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

// Maps a StoredProfile to the CreatedProfile the DeployForm needs. It prefers
// the server-derived identity fields and falls back to the raw profile
// document. The raw JSON is kept so the admin can see what is stored.
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

// What Deployment takes from a stored profile, so the admin never retypes it
interface CreatedProfile {
  profileDigest: string;
  cid: string;
  projectId: string;
  tokenDecimals: number;
  tokenUnit: string;
  /** The stored profile JSON, pretty-printed and shown read-only */
  rawProfileJson?: string;
}

// The profile format version is fixed at 1.0 and ProfileEditor fills in the
// projectId from the server config, so the operator enters only the descriptive
// fields and the per-asset schema
const PROFILE_VERSION = "1.0";

// Starter per-asset JSON Schema: an empty object schema the operator fills in.
// The server validates it against its closed dialect when Validate runs.
const DEFAULT_ASSET_SCHEMA = JSON.stringify(
  {
    type: "object",
    properties: {},
    required: [],
  },
  null,
  2,
);

// Reads the fields Deployment needs from a raw profile; the server owns the stored digest
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
  // The version is fixed, and projectId comes from the server config
  // (contract.project_id), so the operator enters neither. The page reads
  // projectId from GET /api/v1/config to match the value the server checks
  // every profile against. The other values are separate labelled fields.
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

  // Load the deployment's projectId from the server. An empty value means
  // contract.project_id is not set, which blocks profile creation
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

  // Assembles the profile document from the form fields and parses the one
  // free-form part, assetSchema. The server decides validity.
  function buildProfileOrError(): Record<string, unknown> | undefined {
    if (!projectId) {
      setError(
        "No projectId is configured on the server yet - set contract.project_id in the server config before creating a profile.",
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
        // A 400 from POST /api/v1/profile carries a ValidationResult (errors[]),
        // not the generic Error body, so read whichever shape came back
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
            <dd>Persisted - immutable for the life of this deployment</dd>
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
              This stored profile is immutable for the deployment&apos;s
              lifetime and is shown read-only.
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
        The server supplies the project ID (contract.project_id); profile
        version 1.0 is fixed. If the ID is &quot;not configured&quot;, set it in
        the server config before creating a profile.
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
          real-estate, invoice). 1-128 characters.
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
          The unit one whole token stands for (e.g. gram, sqft, USD). 1-64
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
          How many fractional digits the token supports (0-36; e.g. 18, like
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
          Deed number, Invoice ID). Shown in record forms. 1-128 characters.
        </span>
      </div>

      <fieldset className="field">
        <legend>Legal descriptor (optional)</legend>
        <span className="field__hint" id="legal-hint">
          Describe the instrument this token represents. This information is
          fixed with the profile and recorded without verification. Select None
          to omit it.
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
          A JSON Schema for each record&apos;s asset metadata, used to generate
          and validate its form. Enter a schema object; server validation
          enforces the supported dialect.{" "}
          <a
            href="https://json-schema.org/learn/getting-started-step-by-step"
            target="_blank"
            rel="noopener noreferrer"
          >
            JSON Schema documentation
          </a>
          . Validate without saving first. Profile creation is admin-only and
          permanent. The stored profile supplies the deployment&apos;s project
          ID, token unit, decimals and digest.
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

// RWAFactory._validateConfig reverts with InvalidConfig when any of these is the
// zero address, so the form requires all of them
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
  // The admin signs in with a connected wallet; the deploy is broadcast from it,
  // and the quote token's decimals are read on the deployment chain
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
      // The admin's wallet broadcasts the deployment (RWAFactory.deploy is
      // permissionless) and the server observes the ProjectDeployed event. The
      // factory address and chain come from GET /api/v1/config, because
      // GET /project returns 404 before the first deployment.
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

      // Deploying needs a connected wallet; connect now if login left none
      let from = wallet.address;
      if (!from) {
        try {
          from = (await connectWallet()).address;
        } catch {
          setError("Connect your wallet on the deployment chain to deploy.");
          return;
        }
      }

      // The prices are entered in whole quote-token units and ProjectConfig
      // needs minimal units. No Project.quoteDecimals exists before deployment,
      // so read the quote token's decimals() on the deployment chain, which also
      // checks that the wallet is on that chain. An unreadable quote token or an
      // over-precise price blocks the deployment.
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
            : "Invalid price - enter a whole-quote-token amount.",
        );
        return;
      }

      // Assemble ProjectConfig in the browser. projectId is the keccak256 of the
      // profile's UUID string; the server derives the same bytes32 and must
      // match it. decimals and profileDigest come from the stored Asset Profile.
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
      // The server observes ProjectDeployed and moves GET /project through
      // Verifying to Active; the parent's polling shows it
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
            Broadcasts RWAFactory.deploy from your wallet on the deployment
            chain. The server adopts the ProjectDeployed event. Review every
            field: token identity and fixed contract wiring are permanent.
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
          Deployment broadcast for project <strong>{profile.projectId}</strong>.
          Transaction <span className="mono">{txHash}</span>. Waiting for the
          server to observe it - see <strong>Current project</strong> above.
        </p>
      )}
    </div>
  );
}
