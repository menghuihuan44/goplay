package com.goplay.mobile

import android.app.Application

class GoPlayApp : Application() {
    lateinit var graph: AppGraph
        private set

    override fun onCreate() {
        super.onCreate()
        graph = AppGraph(this)
        graph.logs.log("GoPlay Mobile 启动")
    }
}
