import { ReactFlowProvider } from '@xyflow/react'
import { useEffect } from 'react'
import { BrowserRouter, Route, Routes } from 'react-router'
import { Login } from '@/app/Login'
import { Shell } from '@/app/Shell'
import { Agents } from '@/features/agents/Agents'
import { Directory } from '@/features/directory/Directory'
import { LiveGraph } from '@/features/graph/LiveGraph'
import { Inbox } from '@/features/inbox/Inbox'
import { TaskView } from '@/features/task/TaskView'
import { useSession } from '@/store/session'

export function App() {
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
          <Route path="tasks/:id" element={<ReactFlowProvider key="task"><TaskView /></ReactFlowProvider>} />
          <Route path="agents" element={<Agents />} />
          <Route path="directory" element={<Directory />} />
        </Route>
      </Routes>
    </BrowserRouter>
  )
}
