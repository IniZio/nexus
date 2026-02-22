import React, { useState, useEffect } from 'react'

const API_URL = import.meta.env.VITE_API_URL || 'http://localhost:3000'

function App() {
  const [users, setUsers] = useState([])
  const [health, setHealth] = useState(null)
  const [newUser, setNewUser] = useState({ name: '', email: '' })
  const [cacheTest, setCacheTest] = useState(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetchHealth()
    fetchUsers()
  }, [])

  const fetchHealth = async () => {
    try {
      const res = await fetch(`${API_URL}/health`)
      const data = await res.json()
      setHealth(data)
    } catch (err) {
      setHealth({ status: 'unreachable', error: err.message })
    }
  }

  const fetchUsers = async () => {
    try {
      setLoading(true)
      const res = await fetch(`${API_URL}/api/users`)
      const data = await res.json()
      setUsers(data.users || [])
    } catch (err) {
      console.error('Failed to fetch users:', err)
    } finally {
      setLoading(false)
    }
  }

  const createUser = async (e) => {
    e.preventDefault()
    try {
      const res = await fetch(`${API_URL}/api/users`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(newUser)
      })
      if (res.ok) {
        setNewUser({ name: '', email: '' })
        fetchUsers()
      }
    } catch (err) {
      console.error('Failed to create user:', err)
    }
  }

  const testCache = async () => {
    try {
      const res = await fetch(`${API_URL}/api/users/cache/test`)
      const data = await res.json()
      setCacheTest(data)
    } catch (err) {
      setCacheTest({ error: err.message })
    }
  }

  return (
    <div style={{ maxWidth: '800px', margin: '0 auto', padding: '20px', fontFamily: 'system-ui' }}>
      <h1>🚀 Full-Stack Demo</h1>
      <p>Nexus Workspace Multi-Service Example</p>

      {/* Health Status */}
      <div style={{ 
        padding: '15px', 
        marginBottom: '20px', 
        borderRadius: '8px',
        backgroundColor: health?.status === 'healthy' ? '#d4edda' : '#f8d7da'
      }}>
        <h3>API Health</h3>
        <p><strong>Status:</strong> {health?.status || 'Checking...'}</p>
        {health?.timestamp && (
          <p><small>Last checked: {new Date(health.timestamp).toLocaleTimeString()}</small></p>
        )}
        <button onClick={fetchHealth}>Refresh Health</button>
      </div>

      {/* Cache Test */}
      <div style={{ 
        padding: '15px', 
        marginBottom: '20px', 
        borderRadius: '8px',
        backgroundColor: '#e7f3ff'
      }}>
        <h3>Redis Cache Test</h3>
        <button onClick={testCache}>Test Cache</button>
        {cacheTest && (
          <div style={{ marginTop: '10px' }}>
            <p><strong>Value:</strong> {cacheTest.value}</p>
            <p><strong>Cached:</strong> {cacheTest.cached ? 'Yes (from cache)' : 'No (new value)'}</p>
          </div>
        )}
      </div>

      {/* Add User Form */}
      <div style={{ 
        padding: '15px', 
        marginBottom: '20px', 
        borderRadius: '8px',
        backgroundColor: '#f0f0f0'
      }}>
        <h3>Add New User</h3>
        <form onSubmit={createUser} style={{ display: 'flex', gap: '10px', flexWrap: 'wrap' }}>
          <input
            type="text"
            placeholder="Name"
            value={newUser.name}
            onChange={(e) => setNewUser({...newUser, name: e.target.value})}
            style={{ padding: '8px', flex: '1', minWidth: '150px' }}
          />
          <input
            type="email"
            placeholder="Email"
            value={newUser.email}
            onChange={(e) => setNewUser({...newUser, email: e.target.value})}
            style={{ padding: '8px', flex: '1', minWidth: '150px' }}
          />
          <button type="submit" style={{ padding: '8px 16px' }}>Add User</button>
        </form>
      </div>

      {/* Users List */}
      <div>
        <h3>Users ({users.length})</h3>
        {loading ? (
          <p>Loading users...</p>
        ) : users.length === 0 ? (
          <p>No users found</p>
        ) : (
          <ul style={{ listStyle: 'none', padding: 0 }}>
            {users.map(user => (
              <li key={user.id} style={{ 
                padding: '10px', 
                marginBottom: '5px',
                backgroundColor: '#f9f9f9',
                borderRadius: '4px'
              }}>
                <strong>{user.name}</strong>
                <br />
                <small>{user.email}</small>
                <br />
                <small style={{ color: '#666' }}>
                  Created: {new Date(user.created_at).toLocaleDateString()}
                </small>
              </li>
            ))}
          </ul>
        )}
        <button onClick={fetchUsers}>Refresh Users</button>
      </div>

      <hr style={{ margin: '30px 0' }} />
      
      <footer style={{ color: '#666', fontSize: '14px' }}>
        <p>Running in Nexus Workspace with Docker Compose</p>
        <p>Services: PostgreSQL, Redis, Node.js API, React Frontend</p>
      </footer>
    </div>
  )
}

export default App
