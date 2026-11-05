// Admin password login (DESIGN.md §3.8).

import { useState, type FormEvent } from 'react';
import { api, ApiError, errorMessage } from '../api';
import { BrandMark } from '../components/AppHeader';
import { Button } from '../components/Button';
import { TextField } from '../components/form';
import { Icon } from '../components/Icon';

interface LoginProps {
  hubName: string;
  onDone: () => void;
}

export function LoginPage({ hubName, onDone }: LoginProps) {
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [fieldError, setFieldError] = useState('');
  const [formError, setFormError] = useState('');

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!password) {
      setFieldError('Enter the admin password.');
      return;
    }
    setBusy(true);
    setFieldError('');
    setFormError('');
    try {
      await api.login(password);
      onDone();
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) setFieldError('Wrong password. Check it and try again.');
      else if (err instanceof ApiError && err.status === 429)
        setFormError('Too many failed attempts from this address. Wait about 10 minutes, then try again.');
      else setFormError(errorMessage(err));
      setPassword('');
      setBusy(false);
    }
  };

  return (
    <main className="auth-page">
      <div className="auth-brand">
        <div className="brand-row">
          <BrandMark large />
          <span className="brand-name">NextBoard</span>
        </div>
        <p>{hubName ? `Sign in to hub “${hubName}”` : 'Sign in to your hub'}</p>
      </div>
      <form className="auth-card form" onSubmit={submit} noValidate>
        <fieldset disabled={busy}>
          <TextField
            label="Admin password"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
            autoFocus
            error={fieldError}
          />
        </fieldset>
        {formError && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{formError}</span>
          </div>
        )}
        <Button type="submit" variant="primary" block loading={busy}>
          Log in
        </Button>
        <p className="auth-foot">
          Forgot it? Run <code>nextboard reset-password</code> on the server.
        </p>
      </form>
    </main>
  );
}
