document.addEventListener('DOMContentLoaded', function() {
    let table = new DataTable('#metrics-table', {
        serverSide: true,
        ajax: '/api/data',
        deferRender: true,
        pageLength: 50,
        lengthMenu: [50, 100, 250, 500],
        scrollX: true,
        scrollY: 'calc(100vh - 190px)',
        scrollCollapse: true,
        order: [[3, 'desc']], // Sort by created_at by default
        layout: {
            topStart: {
                buttons: [
                    {
                        text: 'Export CSV',
                        className: 'dt-button',
                        action: function(e, dt, button, config) {
                            let params = dt.ajax.params();
                            params.format = 'csv';
                            let queryString = $.param(params);
                            window.location.href = '/api/export?' + queryString;
                        }
                    },
                    {
                        text: 'Export Excel',
                        className: 'dt-button',
                        action: function(e, dt, button, config) {
                            let params = dt.ajax.params();
                            params.format = 'excel';
                            let queryString = $.param(params);
                            window.location.href = '/api/export?' + queryString;
                        }
                    },
                    {
                        text: 'Export Markdown',
                        className: 'dt-button',
                        action: function(e, dt, button, config) {
                            let params = dt.ajax.params();
                            params.format = 'markdown';
                            let queryString = $.param(params);
                            window.location.href = '/api/export?' + queryString;
                        }
                    },
                    {
                        text: 'Export PDF',
                        className: 'dt-button',
                        action: function(e, dt, button, config) {
                            let params = dt.ajax.params();
                            params.format = 'pdf';
                            let queryString = $.param(params);
                            window.location.href = '/api/export?' + queryString;
                        }
                    }
                ]
            },
            topEnd: 'search',
            bottomStart: 'info',
            bottomEnd: 'paging'
        },
        columns: [
            { 
                data: 'id', 
                className: 'col-number',
                width: '50px'
            },
            { 
                data: 'req_id', 
                className: 'col-mono',
                width: '280px'
            },
            { 
                data: 'status',
                render: function(data, type, row) {
                    if (type === 'display') {
                        return `<span class="status-badge status-${data}">${data}</span>`;
                    }
                    return data;
                },
                width: '80px'
            },
            { 
                data: 'created_at',
                className: 'col-mono',
                render: function(data, type, row) {
                    if (type === 'display' || type === 'filter') {
                        return data.replace('T', ' ').replace('Z', '');
                    }
                    return data;
                },
                width: '160px'
            },
            { 
                data: 'duration_ms', 
                className: 'col-number',
                render: function(data, type, row) {
                    if (type === 'display') {
                        return data.toFixed(2);
                    }
                    return data;
                },
                width: '100px'
            },
            { 
                data: 'is_active',
                render: function(data, type, row) {
                    if (type === 'display') {
                        return data ? `<span class="bool-true">✔</span>` : `<span class="bool-false">✘</span>`;
                    }
                    return data ? 'Active' : 'Inactive';
                },
                width: '60px',
                className: 'dt-center'
            },
            { 
                data: 'category',
                width: '100px'
            },
            { 
                data: 'metadata', 
                className: 'col-mono',
                orderable: false,
                render: function(data, type, row) {
                    if (type === 'display') {
                        if (data && data.length > 50) {
                            return `<span title="${data.replace(/"/g, '&quot;')}">${data.substring(0, 47)}...</span>`;
                        }
                    }
                    return data;
                }
            }
        ]
    });
});
