import React, { useState, useEffect } from 'react';
import './App.css';

function App() {
  const [count, setCount] = useState(0);
  const [message, setMessage] = useState('Initial load');
  const [users, setUsers] = useState([]);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    document.title = `Count: ${count}`;
  }, [count]);

  const handleIncrement = () => {
    setCount(prev => prev + 1);
    setMessage('Counter incremented');
  };

  const handleDecrement = () => {
    setCount(prev => prev - 1);
    setMessage('Counter decremented');
  };

  const fetchUsers = async () => {
    setLoading(true);
    try {
      const response = await fetch('http://localhost:3000/api/users');
      const data = await response.json();
      setUsers(data);
      setMessage('Users loaded successfully');
    } catch (error) {
      setMessage('Failed to load users');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="App">
      <header className="App-header">
        <h1>React Hot Reload Test</h1>
        <div className="counter-section">
          <p>Count: {count}</p>
          <button onClick={handleIncrement}>Increment</button>
          <button onClick={handleDecrement}>Decrement</button>
        </div>
        <div className="status-section">
          <p>Status: {message}</p>
        </div>
        <div className="users-section">
          <button onClick={fetchUsers} disabled={loading}>
            {loading ? 'Loading...' : 'Load Users'}
          </button>
          {users.length > 0 && (
            <ul>
              {users.map(user => (
                <li key={user.id}>{user.name} - {user.email}</li>
              ))}
            </ul>
          )}
        </div>
      </header>
    </div>
  );
}

export default App;
