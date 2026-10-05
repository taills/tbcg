import type { FormEvent, ReactElement } from 'react';

interface LoginFormProps {
  pending: boolean;
  error: string | null;
  onLogin: (username: string, password: string) => Promise<void>;
}

export default function LoginForm({ pending, error, onLogin }: LoginFormProps): ReactElement {
  const handleSubmit = (event: FormEvent<HTMLFormElement>): void => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const username = form.get('username');
    const password = form.get('password');
    if (typeof username === 'string' && typeof password === 'string') {
      void onLogin(username.trim(), password);
    }
  };

  return (
    <section className="mx-auto mt-14 w-full max-w-md rounded-xl border border-slate-200 bg-white p-6 shadow-sm">
      <div className="mb-6 border-b border-slate-100 pb-5">
        <p className="text-xs font-semibold uppercase tracking-[0.18em] text-teal-700">
          TBCG · CONTENT CONTROL
        </p>
        <h1 className="mt-2 text-2xl font-semibold tracking-tight text-slate-950">管理端登录</h1>
        <p className="mt-2 text-sm leading-6 text-slate-600">使用受控管理账号进入内容工作台。</p>
      </div>
      <form className="space-y-4" onSubmit={handleSubmit}>
        <div>
          <label
            className="mb-1.5 block text-sm font-medium text-slate-800"
            htmlFor="admin-username"
          >
            用户名
          </label>
          <input
            autoComplete="username"
            className="w-full rounded-md border border-slate-300 bg-white px-3 py-2.5 text-sm text-slate-950 outline-none transition focus:border-teal-700 focus:ring-2 focus:ring-teal-700/20"
            id="admin-username"
            maxLength={128}
            name="username"
            required
          />
        </div>
        <div>
          <label
            className="mb-1.5 block text-sm font-medium text-slate-800"
            htmlFor="admin-password"
          >
            密码
          </label>
          <input
            autoComplete="current-password"
            className="w-full rounded-md border border-slate-300 bg-white px-3 py-2.5 text-sm text-slate-950 outline-none transition focus:border-teal-700 focus:ring-2 focus:ring-teal-700/20"
            id="admin-password"
            maxLength={72}
            name="password"
            required
            type="password"
          />
          <p className="mt-1 text-xs text-slate-500">最多 72 个 UTF-8 字节。</p>
        </div>
        {error !== null && (
          <p
            className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800"
            role="alert"
          >
            {error}
          </p>
        )}
        <button
          className="w-full rounded-md bg-teal-800 px-4 py-2.5 text-sm font-semibold text-white transition hover:bg-teal-900 focus:outline-none focus:ring-2 focus:ring-teal-700 focus:ring-offset-2 disabled:cursor-wait disabled:opacity-60"
          disabled={pending}
          type="submit"
        >
          {pending ? '正在验证…' : '登录'}
        </button>
      </form>
    </section>
  );
}
