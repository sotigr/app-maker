import { useState } from "react";
import { Api, describeError } from "../api";

export function Login({ onLogin }: { onLogin: () => void }) {
  const [login, setLogin] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState("");
  return (
    <form
      className="card login"
      onSubmit={(e) => {
        e.preventDefault();
        setErr("");
        Api.login(login, password).then(onLogin, (x) => setErr(describeError(x)));
      }}
    >
      <h1>Sign in</h1>
      <label>Email</label>
      <input value={login} onChange={(e) => setLogin(e.target.value)} autoFocus />
      <label>Password</label>
      <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
      <p className="error">{err}</p>
      <button className="primary" type="submit">
        Sign in
      </button>
    </form>
  );
}
