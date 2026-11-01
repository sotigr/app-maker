import { useEffect, useState } from "react";
import { Api, describeError } from "../api";

type Step = { kind: "password" } | { kind: "verify"; ticket?: string } | { kind: "enroll"; ticket?: string; secret?: string; uri?: string };

// Sign-in: password or OIDC, then a TOTP code when MFA applies (spec 8.3).
export function Login({ onLogin }: { onLogin: () => void }) {
  const mfaParam = new URLSearchParams(location.search).get("mfa");
  const [step, setStep] = useState<Step>(
    mfaParam === "verify" ? { kind: "verify" } : mfaParam === "enroll" ? { kind: "enroll" } : { kind: "password" },
  );
  const [login, setLogin] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [providers, setProviders] = useState<string[]>([]);
  const [err, setErr] = useState("");
  const fail = (x: unknown) => setErr(describeError(x));

  useEffect(() => {
    Api.providers().then((r) => setProviders(r.providers), () => setProviders([]));
  }, []);
  useEffect(() => {
    if (step.kind === "enroll" && !step.secret) {
      Api.enrollTotp(step.ticket).then((e) => setStep({ kind: "enroll", ticket: e.ticket, secret: e.secret, uri: e.uri }), fail);
    }
  }, [step]);

  const done = () => {
    history.replaceState(null, "", location.pathname + location.hash);
    onLogin();
  };
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setErr("");
    if (step.kind === "password") {
      Api.login(login, password).then((r) => {
        if (r.mfa === "verify") setStep({ kind: "verify", ticket: r.ticket });
        else if (r.mfa === "enroll") setStep({ kind: "enroll", ticket: r.ticket });
        else done();
      }, fail);
    } else if (step.kind === "verify") {
      Api.verifyMfa(code, step.ticket).then(done, fail);
    } else {
      Api.confirmTotp(code, step.ticket).then(done, fail);
    }
  };

  return (
    <form className="card login" onSubmit={submit}>
      <h1>Sign in</h1>
      {step.kind === "password" && (
        <>
          <label>Email</label>
          <input value={login} onChange={(e) => setLogin(e.target.value)} autoFocus />
          <label>Password</label>
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </>
      )}
      {step.kind === "enroll" && (
        <>
          <p>Two-factor authentication is required. Add this key to your authenticator app, then enter a code.</p>
          <code className="secret">{step.secret || "…"}</code>
          {step.uri && (
            <p className="muted">
              <a href={step.uri}>Open in authenticator app</a>
            </p>
          )}
        </>
      )}
      {step.kind !== "password" && (
        <>
          <label>Authentication code</label>
          <input value={code} onChange={(e) => setCode(e.target.value.trim())} inputMode="numeric" autoComplete="one-time-code" autoFocus />
        </>
      )}
      <p className="error">{err}</p>
      <button className="primary" type="submit">
        {step.kind === "password" ? "Sign in" : "Verify"}
      </button>
      {step.kind === "password" &&
        providers.map((p) => (
          <a key={p} className="button" href={`/v1/auth/oidc/${encodeURIComponent(p)}/start?redirect=${encodeURIComponent("/admin/")}`}>
            Sign in with {p}
          </a>
        ))}
    </form>
  );
}
