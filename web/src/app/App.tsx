import { ReactFlowProvider } from '@xyflow/react'
import { useEffect } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useParams } from 'react-router'
import { Login } from '@/app/Login'
import { savedTheme, Shell } from '@/app/Shell'
import { Agents } from '@/features/agents/Agents'
import { Decision } from '@/features/decision/Decision'
import { Directory } from '@/features/directory/Directory'
import { LiveGraph } from '@/features/graph/LiveGraph'
import { Inbox } from '@/features/inbox/Inbox'
import { Runs } from '@/features/runs/Runs'
import { Settings } from '@/features/settings/Settings'
import { useSession } from '@/store/session'

/** A decision link (/d/<token>) works without logging in: the token is the credential. */
const decisionToken = /^\/d\/([^/]+)\/?$/.exec(location.pathname)?.[1]

// Pages outside the shell (login, decision links) follow the theme too.
document.documentElement.dataset.theme = savedTheme()

export function App() {
  if (decisionToken) return <Decision token={decisionToken} />
  return <LoggedIn />
}

function LoggedIn() {
  const { ready, user, load } = useSession()
  useEffect(() => {
    void load()
  }, [load])
  if (!ready) return null
  if (!user) return <Login />
  return (
    <BrowserRouter>
      <Routes>
        <Route element={<Shell />}>
          <Route index element={<ReactFlowProvider><LiveGraph /></ReactFlowProvider>} />
          <Route path="inbox" element={<Inbox />} />
          <Route path="inbox/:handle" element={<Inbox />} />
          <Route path="runs" element={<Runs />} />
          <Route path="runs/:id" element={<Runs />} />
          <Route path="tasks/:id" element={<TaskRedirect />} />
          <Route path="agents" element={<Agents />} />
          <Route path="directory" element={<Directory />} />
          <Route path="settings" element={<Settings />} />
        </Route>
      </Routes>
    </BrowserRouter>
  )
}

/** Old task links open the run the task belongs to, at its first step. */
function TaskRedirect() {
  const { id = '' } = useParams()
  return <Navigate to={`/runs/${id}?task=${id}`} replace />
}
