document.addEventListener('DOMContentLoaded', function() {
    fetch('/api/schema')
        .then(response => response.json())
        .then(columns => {
            if (!columns || columns.length === 0) {
                alert('No columns found in database');
                return;
            }

            const theadTr = document.querySelector('#metrics-table thead tr');
            theadTr.innerHTML = '';
            
            const dtColumns = [];

            columns.forEach(col => {
                const th = document.createElement('th');
                th.textContent = col.name;
                theadTr.appendChild(th);

                let renderFunc = null;
                let className = '';
                
                const cType = (col.type || '').toUpperCase();

                if (cType.includes('TIMESTAMP') || cType.includes('DATE')) {
                    className = 'col-mono';
                    renderFunc = function(data, type, row) {
                        if (data && (type === 'display' || type === 'filter')) {
                            return String(data).replace('T', ' ').replace('Z', '');
                        }
                        return data;
                    };
                } else if (cType.includes('INT') || cType.includes('DOUBLE') || cType.includes('FLOAT')) {
                    className = 'col-number';
                    if (cType.includes('DOUBLE') || cType.includes('FLOAT')) {
                        renderFunc = function(data, type, row) {
                            if (typeof data === 'number' && type === 'display') {
                                return data.toFixed(2);
                            }
                            return data;
                        };
                    }
                } else if (cType === 'BOOLEAN') {
                    className = 'dt-center';
                    renderFunc = function(data, type, row) {
                        if (type === 'display') {
                            return data ? `<span class="bool-true">✔</span>` : `<span class="bool-false">✘</span>`;
                        }
                        return data ? 'True' : 'False';
                    };
                } else if (col.name.toLowerCase() === 'status') {
                    renderFunc = function(data, type, row) {
                        if (type === 'display' && data) {
                            return `<span class="status-badge status-${data}">${data}</span>`;
                        }
                        return data;
                    };
                } else {
                    // Default string / varchar / json handling
                    if (col.name.toLowerCase() === 'metadata') {
                         className = 'col-mono';
                    }
                    renderFunc = function(data, type, row) {
                        if (type === 'display' && data) {
                            let str = typeof data === 'object' ? JSON.stringify(data) : String(data);
                            if (str.length > 50) {
                                return `<span title="${str.replace(/"/g, '&quot;')}">${str.substring(0, 47)}...</span>`;
                            }
                            return str;
                        }
                        return data;
                    };
                }

                dtColumns.push({
                    data: col.name,
                    className: className,
                    render: renderFunc,
                    defaultContent: ''
                });
            });

            let table = new DataTable('#metrics-table', {
                serverSide: true,
                ajax: '/api/data',
                deferRender: true,
                pageLength: 50,
                lengthMenu: [50, 100, 250, 500],
                scrollX: true,
                scrollY: 'calc(100vh - 190px)',
                scrollCollapse: true,
                order: [], // default natural order
                layout: {
                    topStart: {
                        buttons: [
                            {
                                text: 'Export CSV',
                                className: 'dt-button',
                                action: function(e, dt, button, config) {
                                    let params = dt.ajax.params();
                                    params.format = 'csv';
                                    window.location.href = '/api/export?' + $.param(params);
                                }
                            },
                            {
                                text: 'Export Excel',
                                className: 'dt-button',
                                action: function(e, dt, button, config) {
                                    let params = dt.ajax.params();
                                    params.format = 'excel';
                                    window.location.href = '/api/export?' + $.param(params);
                                }
                            },
                            {
                                text: 'Export Markdown',
                                className: 'dt-button',
                                action: function(e, dt, button, config) {
                                    let params = dt.ajax.params();
                                    params.format = 'markdown';
                                    window.location.href = '/api/export?' + $.param(params);
                                }
                            },
                            {
                                text: 'Export PDF',
                                className: 'dt-button',
                                action: function(e, dt, button, config) {
                                    let params = dt.ajax.params();
                                    params.format = 'pdf';
                                    window.location.href = '/api/export?' + $.param(params);
                                }
                            }
                        ]
                    },
                    topEnd: 'search',
                    bottomStart: 'info',
                    bottomEnd: 'paging'
                },
                columns: dtColumns
            });
        })
        .catch(err => {
            console.error('Failed to fetch schema:', err);
            alert('Failed to load table schema');
        });
});
